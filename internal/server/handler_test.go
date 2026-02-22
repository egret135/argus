package server

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/admin/argus/internal/model"
	"github.com/admin/argus/internal/pipeline"
	"github.com/admin/argus/internal/storage"
)

type mockPipeline struct {
	ring  *storage.RingBuffer
	index *storage.IndexSnapshot
	stats *pipeline.Stats
}

func (m *mockPipeline) GetIndex() *storage.IndexSnapshot { return m.index }
func (m *mockPipeline) GetRingBuffer() *storage.RingBuffer { return m.ring }
func (m *mockPipeline) GetStats() *pipeline.Stats          { return m.stats }

func newTestHandler(t *testing.T, mp PipelineReader) *Handler {
	t.Helper()
	tmpl := template.Must(template.New("admin.html").Parse("{{.CSRFToken}}"))
	tmpl = template.Must(tmpl.New("login.html").Parse("{{.CSRFToken}}"))

	auth, err := NewAuth("admin", "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234", t.TempDir(), "test-secret")
	if err != nil {
		t.Fatalf("NewAuth: %v", err)
	}
	return NewHandler(mp, auth, tmpl)
}

func TestHandleHealthz(t *testing.T) {
	ring := storage.NewRingBuffer(100)
	mp := &mockPipeline{ring: ring, stats: &pipeline.Stats{}}
	h := newTestHandler(t, mp)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.HandleHealthz(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("expected body 'ok', got %q", rec.Body.String())
	}
}

func TestHandleReadyz(t *testing.T) {
	ring := storage.NewRingBuffer(100)
	mp := &mockPipeline{ring: ring, stats: &pipeline.Stats{}}
	h := newTestHandler(t, mp)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.HandleReadyz(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ready" {
		t.Fatalf("expected body 'ready', got %q", rec.Body.String())
	}
}

func TestHandleReadyz_NotReady(t *testing.T) {
	mp := &mockPipeline{ring: nil, stats: &pipeline.Stats{}}
	h := newTestHandler(t, mp)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.HandleReadyz(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestHandleAPIStats(t *testing.T) {
	ring := storage.NewRingBuffer(100)
	stats := &pipeline.Stats{}
	stats.IngestTotal.Store(42)
	mp := &mockPipeline{ring: ring, stats: stats}
	h := newTestHandler(t, mp)

	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	rec := httptest.NewRecorder()
	h.HandleAPIStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]int64
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	expectedKeys := []string{
		"ingest_total", "ingest_dropped", "parse_error", "docker_parse_error",
		"wal_bytes", "wal_compaction_total", "wal_truncated",
		"alert_send_ok", "alert_send_failed", "alert_queue_full",
		"alert_inflight_timeout", "alert_deduplicated", "alert_throttled",
		"tailer_reconnects", "index_rebuild_total", "index_rebuild_duration_ms",
		"ringbuffer_head", "ringbuffer_tail", "ringbuffer_capacity",
	}
	for _, key := range expectedKeys {
		if _, ok := result[key]; !ok {
			t.Errorf("missing key %q in stats response", key)
		}
	}
	if result["ingest_total"] != 42 {
		t.Fatalf("expected ingest_total=42, got %d", result["ingest_total"])
	}
}

func TestHandleAPILogs(t *testing.T) {
	ring := storage.NewRingBuffer(100)
	ring.Write(model.LogEntry{
		Timestamp: "2026-02-22T10:30:00.123+08:00",
		Level:     "ERROR",
		Service:   "test-svc",
		Message:   "test error message",
		Source:    "test-source",
	})
	mp := &mockPipeline{ring: ring, stats: &pipeline.Stats{}}
	h := newTestHandler(t, mp)

	req := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	rec := httptest.NewRecorder()
	h.HandleAPILogs(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result struct {
		Logs    []json.RawMessage `json:"logs"`
		HasMore bool              `json:"has_more"`
		Expired bool              `json:"expired"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(result.Logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(result.Logs))
	}
	if result.HasMore {
		t.Fatal("expected has_more=false")
	}
	if result.Expired {
		t.Fatal("expected expired=false")
	}
}
