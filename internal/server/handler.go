package server

import (
	"encoding/json"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/admin/argus/internal/model"
	"github.com/admin/argus/internal/pipeline"
	"github.com/admin/argus/internal/storage"
)

// PipelineReader is the interface that Handler uses to access pipeline data,
// avoiding a direct dependency on the concrete Pipeline struct.
type PipelineReader interface {
	GetIndex() *storage.IndexSnapshot
	GetRingBuffer() *storage.RingBuffer
	GetStats() *pipeline.Stats
}

// Handler contains the HTTP handlers for the Argus web interface and API.
type Handler struct {
	pipeline  PipelineReader
	auth      *Auth
	templates *template.Template
}

// NewHandler creates a new Handler.
func NewHandler(p PipelineReader, auth *Auth, templates *template.Template) *Handler {
	return &Handler{pipeline: p, auth: auth, templates: templates}
}

// HandleLogin serves the login page (GET) and processes login attempts (POST).
func (h *Handler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		csrfToken := h.auth.GenerateCSRFToken()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		h.templates.ExecuteTemplate(w, "login.html", map[string]any{
			"CSRFToken": csrfToken,
		})

	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		csrfToken := r.FormValue("csrf_token")
		if !h.auth.ValidateCSRFToken(csrfToken) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		username := r.FormValue("username")
		password := r.FormValue("password")
		if username != h.auth.username || !h.auth.CheckPassword(password) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			newToken := h.auth.GenerateCSRFToken()
			h.templates.ExecuteTemplate(w, "login.html", map[string]any{
				"CSRFToken": newToken,
				"Error":     true,
			})
			return
		}
		token, err := h.auth.GenerateToken()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "argus_token",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   86400,
		})
		http.Redirect(w, r, "/admin", http.StatusFound)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleLogout clears the auth cookie and redirects to /login.
func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "argus_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// HandleAdmin renders the admin log viewer page.
func (h *Handler) HandleAdmin(w http.ResponseWriter, r *http.Request) {
	csrfToken := h.auth.GenerateCSRFToken()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.templates.ExecuteTemplate(w, "admin.html", map[string]any{
		"CSRFToken": csrfToken,
	})
}

// HandleLogDetail renders the log detail page for a specific log entry.
func (h *Handler) HandleLogDetail(w http.ResponseWriter, r *http.Request) {
	seqStr := r.URL.Query().Get("seq_id")
	if seqStr == "" {
		http.Error(w, "missing seq_id", http.StatusBadRequest)
		return
	}
	seqID, err := strconv.ParseUint(seqStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid seq_id", http.StatusBadRequest)
		return
	}

	rb := h.pipeline.GetRingBuffer()
	entry, ok := rb.Read(seqID)
	if !ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		h.templates.ExecuteTemplate(w, "log_detail.html", map[string]any{
			"NotFound": true,
			"SeqID":    seqID,
		})
		return
	}

	extraJSON := ""
	if len(entry.Extra) > 0 {
		if b, err := json.MarshalIndent(entry.Extra, "", "  "); err == nil {
			extraJSON = string(b)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.templates.ExecuteTemplate(w, "log_detail.html", map[string]any{
		"SeqID":      entry.SeqID,
		"Timestamp":  entry.Timestamp,
		"Level":      entry.Level,
		"Service":    entry.Service,
		"Source":     entry.Source,
		"Message":    entry.Message,
		"TraceID":    entry.TraceID,
		"Caller":     entry.Caller,
		"StackTrace": entry.StackTrace,
		"Extra":      extraJSON,
	})
}

// logResponse is the JSON envelope for the logs API.
type logResponse struct {
	Logs    []logEntry `json:"logs"`
	HasMore bool       `json:"has_more"`
	Expired bool       `json:"expired"`
}

// logEntry is the JSON representation of a single log entry.
type logEntry struct {
	SeqID      uint64         `json:"seq_id"`
	Timestamp  string         `json:"timestamp"`
	Level      string         `json:"level"`
	Service    string         `json:"service"`
	Message    string         `json:"message"`
	TraceID    string         `json:"trace_id,omitempty"`
	Caller     string         `json:"caller,omitempty"`
	StackTrace string         `json:"stack_trace,omitempty"`
	Source     string         `json:"source"`
	Extra      map[string]any `json:"extra,omitempty"`
}

func toLogEntry(e model.LogEntry) logEntry {
	return logEntry{
		SeqID:      e.SeqID,
		Timestamp:  e.Timestamp,
		Level:      e.Level,
		Service:    e.Service,
		Message:    e.Message,
		TraceID:    e.TraceID,
		Caller:     e.Caller,
		StackTrace: e.StackTrace,
		Source:     e.Source,
		Extra:      e.Extra,
	}
}

// HandleAPILogs handles GET /api/logs with cursor-based pagination and
// two-phase search (index + ring buffer scan).
func (h *Handler) HandleAPILogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}

	var beforeSeq, afterSeq uint64
	var hasBefore, hasAfter bool
	if v := q.Get("before_seq"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			beforeSeq = n
			hasBefore = true
		}
	}
	if v := q.Get("after_seq"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			afterSeq = n
			hasAfter = true
		}
	}

	query := q.Get("q")
	level := q.Get("level")
	source := q.Get("source")

	var timeAfter string
	if v := q.Get("time_range"); v != "" {
		if hours, err := strconv.Atoi(v); err == nil && hours > 0 {
			timeAfter = time.Now().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339Nano)
		}
	}

	idx := h.pipeline.GetIndex()
	rb := h.pipeline.GetRingBuffer()

	var matched []model.LogEntry
	expired := false

	hasFilter := query != "" || level != "" || source != ""

	// Phase 1: Index search for [head, TailSeq).
	if idx != nil && hasFilter {
		seqIDs := idx.Search(query, level, source)
		for _, seq := range seqIDs {
			entry, ok := rb.Read(seq)
			if !ok {
				expired = true
				continue
			}
			if timeAfter != "" && entry.Timestamp < timeAfter {
				continue
			}
			matched = append(matched, entry)
		}
	}

	// Phase 2: Scan ring buffer for entries not covered by the index,
	// or the entire buffer when no keyword/level/source filters are set.
	var scanFrom uint64
	if idx != nil && hasFilter {
		scanFrom = idx.TailSeq
	} else {
		scanFrom = rb.Head()
	}
	rbTail := rb.Tail()
	if scanFrom < rbTail {
		rb.RangeRaw(scanFrom, rbTail, 1024, func(batch []model.LogEntry) bool {
			for _, e := range batch {
				if matchesFilters(e, query, level, source, timeAfter) {
					matched = append(matched, e)
				}
			}
			return true
		})
	}

	// Deduplicate entries by SeqID.
	if len(matched) > 0 {
		seen := make(map[uint64]struct{}, len(matched))
		deduped := matched[:0]
		for _, e := range matched {
			if _, ok := seen[e.SeqID]; !ok {
				seen[e.SeqID] = struct{}{}
				deduped = append(deduped, e)
			}
		}
		matched = deduped
	}

	// Sort descending by SeqID.
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].SeqID > matched[j].SeqID
	})

	// Apply cursor pagination.
	if hasBefore {
		filtered := matched[:0]
		for _, e := range matched {
			if e.SeqID < beforeSeq {
				filtered = append(filtered, e)
			}
		}
		matched = filtered
	}
	if hasAfter {
		filtered := matched[:0]
		for _, e := range matched {
			if e.SeqID > afterSeq {
				filtered = append(filtered, e)
			}
		}
		matched = filtered
	}

	hasMore := len(matched) > limit
	if hasMore {
		matched = matched[:limit]
	}

	logs := make([]logEntry, len(matched))
	for i, e := range matched {
		logs[i] = toLogEntry(e)
	}

	resp := logResponse{
		Logs:    logs,
		HasMore: hasMore,
		Expired: expired,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// matchesFilters checks whether a log entry matches the given query, level,
// and source filters by simple substring / equality checks.
func matchesFilters(e model.LogEntry, query, level, source, timeAfter string) bool {
	if level != "" && e.Level != level {
		return false
	}
	if source != "" && e.Source != source {
		return false
	}
	if query != "" && !strings.Contains(strings.ToLower(e.Message), strings.ToLower(query)) {
		return false
	}
	if timeAfter != "" && e.Timestamp < timeAfter {
		return false
	}
	return true
}

// HandleAPIStats returns pipeline metrics as JSON.
func (h *Handler) HandleAPIStats(w http.ResponseWriter, r *http.Request) {
	stats := h.pipeline.GetStats()
	rb := h.pipeline.GetRingBuffer()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int64{
		"ingest_total":              stats.IngestTotal.Load(),
		"ingest_dropped":            stats.IngestDropped.Load(),
		"parse_error":               stats.ParseError.Load(),
		"docker_parse_error":        stats.DockerParseError.Load(),
		"wal_bytes":                 stats.WalBytes.Load(),
		"wal_compaction_total":      stats.WalCompactionTotal.Load(),
		"wal_truncated":             stats.WalTruncated.Load(),
		"alert_send_ok":             stats.AlertSendOK.Load(),
		"alert_send_failed":         stats.AlertSendFailed.Load(),
		"alert_queue_full":          stats.AlertQueueFull.Load(),
		"alert_inflight_timeout":    stats.AlertInflightTimeout.Load(),
		"alert_deduplicated":        stats.AlertDeduplicated.Load(),
		"alert_throttled":           stats.AlertThrottled.Load(),
		"tailer_reconnects":         stats.TailerReconnects.Load(),
		"index_rebuild_total":       stats.IndexRebuildTotal.Load(),
		"index_rebuild_duration_ms": stats.IndexRebuildDurationMS.Load(),
		"ringbuffer_head":           int64(rb.Head()),
		"ringbuffer_tail":           int64(rb.Tail()),
		"ringbuffer_capacity":       int64(rb.Capacity()),
	})
}

// HandleAPIDistribution returns level and source distribution counts.
func (h *Handler) HandleAPIDistribution(w http.ResponseWriter, r *http.Request) {
	idx := h.pipeline.GetIndex()

	levelCounts := make(map[string]int)
	sourceCounts := make(map[string]int)

	if idx != nil {
		for level, seqIDs := range idx.LevelIdx {
			levelCounts[level] = len(seqIDs)
		}
		for source, seqIDs := range idx.SourceIdx {
			sourceCounts[source] = len(seqIDs)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"levels":  levelCounts,
		"sources": sourceCounts,
	})
}

// HandleHealthz always returns 200 OK.
func (h *Handler) HandleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// HandleReadyz checks basic readiness and returns 200 or 503.
func (h *Handler) HandleReadyz(w http.ResponseWriter, r *http.Request) {
	if h.pipeline.GetRingBuffer() == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("not ready"))
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ready"))
}
