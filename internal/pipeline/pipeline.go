package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"sync"
	"sync/atomic"
	"time"

	"github.com/admin/argus/internal/alert"
	"github.com/admin/argus/internal/config"
	"github.com/admin/argus/internal/model"
	"github.com/admin/argus/internal/parser"
	"github.com/admin/argus/internal/storage"
)

// Stats holds pipeline-wide metrics counters. All fields are accessed atomically.
type Stats struct {
	IngestTotal      atomic.Int64
	IngestDropped    atomic.Int64
	ParseError       atomic.Int64
	DockerParseError atomic.Int64

	WalBytes           atomic.Int64
	WalCompactionTotal atomic.Int64
	WalTruncated       atomic.Int64

	AlertSendOK          atomic.Int64
	AlertSendFailed      atomic.Int64
	AlertQueueFull       atomic.Int64
	AlertInflightTimeout atomic.Int64
	AlertDeduplicated    atomic.Int64
	AlertThrottled       atomic.Int64

	TailerReconnects atomic.Int64

	IndexRebuildTotal      atomic.Int64
	IndexRebuildDurationMS atomic.Int64
}

// Pipeline is the central processing loop that connects log ingestion, WAL
// persistence, ring buffer storage, index building, and alert dispatch.
type Pipeline struct {
	ring   *storage.RingBuffer
	wal    *storage.WAL
	fpRing *storage.FingerprintRing

	limiter *alert.Limiter

	indexPtr          atomic.Pointer[storage.IndexSnapshot]
	rebuildInProgress atomic.Bool

	logChan          <-chan model.RawLog
	alertRequestChan chan model.AlertRequest
	alertResultChan  chan model.AlertResult

	sources map[string]string // sourceID -> type

	cfg               *config.Config
	stats             *Stats
	checkpointDataDir string

	cursorsMu sync.Mutex
	cursors   map[string]json.RawMessage
}

// NewPipeline creates a Pipeline wired to the provided storage components and log channel.
func NewPipeline(cfg *config.Config, ring *storage.RingBuffer, wal *storage.WAL, fpRing *storage.FingerprintRing, logChan <-chan model.RawLog) *Pipeline {
	sources := make(map[string]string, len(cfg.Sources))
	for _, src := range cfg.Sources {
		switch src.Type {
		case "file":
			sources[src.Path] = src.Type
		case "docker":
			sources[src.Container] = src.Type
		}
	}

	return &Pipeline{
		ring:              ring,
		wal:               wal,
		fpRing:            fpRing,
		limiter:           alert.NewLimiter(cfg.Alert.Cooldown),
		logChan:           logChan,
		alertRequestChan:  make(chan model.AlertRequest, 64),
		alertResultChan:   make(chan model.AlertResult, 64),
		sources:           sources,
		cfg:               cfg,
		stats:             &Stats{},
		checkpointDataDir: cfg.Storage.DataDir,
		cursors:           make(map[string]json.RawMessage),
	}
}

// Run is the main processing loop. It blocks until ctx is cancelled.
func (p *Pipeline) Run(ctx context.Context) {
	checkpointTicker := time.NewTicker(p.cfg.Storage.CheckpointInterval)
	defer checkpointTicker.Stop()

	rebuildInterval := p.cfg.Storage.IndexRebuildMaxInterval
	if rebuildInterval <= 0 {
		rebuildInterval = 30 * time.Second
	}
	rebuildTicker := time.NewTicker(rebuildInterval)
	defer rebuildTicker.Stop()

	var entriesSinceRebuild int

	for {
		// 1. Priority drain alertResultChan.
		p.drainAlertResults()

		// 2. Scan inflight timeouts.
		timedOut := p.limiter.CleanupInflight()
		if timedOut > 0 {
			p.stats.AlertInflightTimeout.Add(int64(timedOut))
		}

		// 3. Main select.
		select {
		case raw, ok := <-p.logChan:
			if !ok {
				return
			}
			p.processLog(raw, &entriesSinceRebuild)

		case result := <-p.alertResultChan:
			p.handleAlertResult(result)

		case <-checkpointTicker.C:
			_ = p.wal.Sync()
			p.saveCheckpoint()

		case <-rebuildTicker.C:
			if p.triggerIndexRebuild() {
				entriesSinceRebuild = 0
			}

		case <-ctx.Done():
			// Drain remaining entries from logChan before exiting.
			p.drainLogChan(&entriesSinceRebuild)
			return
		}
	}
}

// processLog handles a single raw log: parse, WAL append, ring write, alert check.
func (p *Pipeline) processLog(raw model.RawLog, entriesSinceRebuild *int) {
	entry, err := parser.Parse(raw.Line, raw.Source)
	if err != nil {
		p.stats.ParseError.Add(1)
		return
	}

	walPayload := storage.EncodeEnvelope(raw.Line, raw.Source)
	if err := p.wal.Append(walPayload); err != nil {
		return
	}
	p.stats.WalBytes.Add(int64(len(walPayload)) + 9)

	entry.SeqID = p.ring.Write(entry)

	if entry.Level == "ERROR" || entry.Level == "FATAL" {
		p.checkAlert(entry)
	}

	p.stats.IngestTotal.Add(1)
	*entriesSinceRebuild++

	if *entriesSinceRebuild >= p.cfg.Storage.IndexRebuildInterval {
		p.triggerIndexRebuild()
		*entriesSinceRebuild = 0
	}

	if p.cfg.Storage.WALCompactThreshold > 0 && p.wal.EntryCount() > p.cfg.Storage.WALCompactThreshold {
		keep := p.cfg.Storage.MaxEntries
		if err := p.wal.Compact(keep); err == nil {
			p.stats.WalCompactionTotal.Add(1)
			_ = p.wal.Sync()
			p.saveCheckpoint()
			p.triggerIndexRebuild()
			*entriesSinceRebuild = 0
		}
	}
}

// drainLogChan drains remaining entries from the log channel after ctx cancellation.
func (p *Pipeline) drainLogChan(entriesSinceRebuild *int) {
	for {
		select {
		case raw, ok := <-p.logChan:
			if !ok {
				return
			}
			p.processLog(raw, entriesSinceRebuild)
		default:
			return
		}
	}
}

// drainAlertResults processes all pending alert results without blocking.
func (p *Pipeline) drainAlertResults() {
	for {
		select {
		case result := <-p.alertResultChan:
			p.handleAlertResult(result)
		default:
			return
		}
	}
}

// handleAlertResult processes a single alert result.
func (p *Pipeline) handleAlertResult(result model.AlertResult) {
	p.limiter.ClearInflight(result.Fingerprint)
	if result.Success {
		p.fpRing.Add(result.Fingerprint)
		p.limiter.SetCooldown(result.CooldownKey)
		p.stats.AlertSendOK.Add(1)
	} else {
		p.stats.AlertSendFailed.Add(1)
	}
}

// checkAlert runs the deduplication and throttling checks, then dispatches an
// alert request if all checks pass.
func (p *Pipeline) checkAlert(entry model.LogEntry) {
	fp := computeFingerprint(entry)

	if p.fpRing.Contains(fp) {
		p.stats.AlertDeduplicated.Add(1)
		return
	}
	if p.limiter.IsInflight(fp) {
		return
	}

	cooldownKey := p.limiter.CooldownKey(entry.Service, entry.Message)
	if p.limiter.IsCoolingDown(cooldownKey) {
		p.stats.AlertThrottled.Add(1)
		return
	}

	select {
	case p.alertRequestChan <- model.AlertRequest{Fingerprint: fp, LogEntry: entry, CooldownKey: cooldownKey}:
		p.limiter.SetInflight(fp)
	default:
		p.stats.AlertQueueFull.Add(1)
	}
}

func truncateMsg(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// computeFingerprint returns a stable fingerprint for a log entry.
// SHA256(source + ":" + timestamp + ":" + CRC32(message)) → first 16 bytes hex.
func computeFingerprint(entry model.LogEntry) string {
	msgCRC := crc32.ChecksumIEEE([]byte(entry.Message))
	data := fmt.Sprintf("%s:%s:%08x", entry.Source, entry.Timestamp, msgCRC)
	sum := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", sum[:16])
}

// triggerIndexRebuild starts an asynchronous index rebuild if one is not
// already in progress. Returns true if a rebuild was started.
func (p *Pipeline) triggerIndexRebuild() bool {
	if !p.rebuildInProgress.CompareAndSwap(false, true) {
		return false
	}

	snapshot, tailSeq := p.ring.SnapshotLite()
	go func() {
		start := time.Now()
		idx := storage.BuildIndex(snapshot, tailSeq)
		p.indexPtr.Store(idx)
		p.rebuildInProgress.Store(false)
		p.stats.IndexRebuildTotal.Add(1)
		p.stats.IndexRebuildDurationMS.Store(time.Since(start).Milliseconds())
	}()
	return true
}

// saveCheckpoint writes a checkpoint to disk with the current cursors,
// WAL offset, and fingerprint ring state.
func (p *Pipeline) saveCheckpoint() {
	p.cursorsMu.Lock()
	cursors := make(map[string]json.RawMessage, len(p.cursors))
	for k, v := range p.cursors {
		cursors[k] = v
	}
	p.cursorsMu.Unlock()

	cp := &storage.Checkpoint{
		Cursors:           cursors,
		WalByteOffset:     p.wal.ByteOffset(),
		AlertFingerprints: p.fpRing.Export(),
	}
	_ = storage.SaveCheckpoint(p.checkpointDataDir, cp)
}

// SeedIndex sets the initial index snapshot. Must be called before Run.
func (p *Pipeline) SeedIndex(idx *storage.IndexSnapshot) {
	p.indexPtr.Store(idx)
}

// GetIndex returns the latest index snapshot (may be nil before first build).
func (p *Pipeline) GetIndex() *storage.IndexSnapshot {
	return p.indexPtr.Load()
}

// GetRingBuffer returns the underlying ring buffer.
func (p *Pipeline) GetRingBuffer() *storage.RingBuffer {
	return p.ring
}

// GetStats returns the pipeline stats.
func (p *Pipeline) GetStats() *Stats {
	return p.stats
}

// AlertRequestChan returns the read end of the alert request channel for workers.
func (p *Pipeline) AlertRequestChan() <-chan model.AlertRequest {
	return p.alertRequestChan
}

// AlertResultChan returns the write end of the alert result channel for workers.
func (p *Pipeline) AlertResultChan() chan<- model.AlertResult {
	return p.alertResultChan
}

// UpdateCursor records the latest cursor position for a source. Thread-safe.
func (p *Pipeline) UpdateCursor(sourceID string, cursor json.RawMessage) {
	p.cursorsMu.Lock()
	p.cursors[sourceID] = cursor
	p.cursorsMu.Unlock()
}

// Shutdown performs a graceful shutdown: drains remaining log lines, fsyncs
// the WAL, and writes a final checkpoint.
func (p *Pipeline) Shutdown() {
	// Drain remaining log lines.
	for {
		select {
		case raw, ok := <-p.logChan:
			if !ok {
				goto drained
			}
			entry, err := parser.Parse(raw.Line, raw.Source)
			if err != nil {
				continue
			}
			_ = p.wal.Append(storage.EncodeEnvelope(raw.Line, raw.Source))
			p.ring.Write(entry)
		default:
			goto drained
		}
	}
drained:
	// Drain remaining alert results.
	for {
		select {
		case result := <-p.alertResultChan:
			p.handleAlertResult(result)
		default:
			goto done
		}
	}
done:
	_ = p.wal.Sync()
	p.saveCheckpoint()
	_ = p.wal.Close()
}
