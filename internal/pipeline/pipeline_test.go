package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/admin/argus/internal/config"
	"github.com/admin/argus/internal/model"
	"github.com/admin/argus/internal/storage"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Storage: config.StorageConfig{
			MaxEntries:              1000,
			DataDir:                 t.TempDir(),
			WALCompactThreshold:     2000,
			CheckpointInterval:      1 * time.Second,
			IndexRebuildInterval:    100,
			IndexRebuildMaxInterval: 30 * time.Second,
		},
		Alert: config.AlertConfig{
			Cooldown:   60 * time.Second,
			MaxRetries: 1,
		},
	}
}

func newTestPipeline(t *testing.T, cfg *config.Config, logChan <-chan model.RawLog) (*Pipeline, *storage.RingBuffer, *storage.WAL) {
	t.Helper()
	ring := storage.NewRingBuffer(cfg.Storage.MaxEntries)
	wal, err := storage.NewWAL(cfg.Storage.DataDir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	fpRing := storage.NewFingerprintRing(1024)
	p := NewPipeline(cfg, ring, wal, fpRing, logChan)
	return p, ring, wal
}

func TestIngestionFlow(t *testing.T) {
	cfg := testConfig(t)
	ch := make(chan model.RawLog, 16)
	p, ring, wal := newTestPipeline(t, cfg, ch)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	logJSON := []byte(`{"timestamp":"2026-02-22T10:30:00.123+08:00","level":"ERROR","service":"test-svc","message":"test error message"}`)
	ch <- model.RawLog{Line: logJSON, Source: "test-source"}

	// Drain the alert request so the pipeline doesn't block.
	go func() {
		for range p.AlertRequestChan() {
		}
	}()

	// Wait for ingestion.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for log to appear in ring buffer")
		default:
		}
		if ring.Len() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	entry, ok := ring.Read(0)
	if !ok {
		t.Fatal("expected to read entry at seq 0")
	}
	if entry.Message != "test error message" {
		t.Fatalf("unexpected message: %q", entry.Message)
	}
	if entry.Level != "ERROR" {
		t.Fatalf("unexpected level: %q", entry.Level)
	}

	if wal.EntryCount() < 1 {
		t.Fatalf("expected WAL entry count >= 1, got %d", wal.EntryCount())
	}
}

func TestIndexRebuild(t *testing.T) {
	cfg := testConfig(t)
	cfg.Storage.IndexRebuildInterval = 5 // rebuild after 5 entries
	ch := make(chan model.RawLog, 256)
	p, _, _ := newTestPipeline(t, cfg, ch)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	// Drain alerts.
	go func() {
		for range p.AlertRequestChan() {
		}
	}()

	for i := 0; i < 10; i++ {
		logJSON := []byte(`{"timestamp":"2026-02-22T10:30:00.123+08:00","level":"INFO","service":"idx-svc","message":"rebuild test message"}`)
		ch <- model.RawLog{Line: logJSON, Source: "test-source"}
	}

	// Wait for index rebuild.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for index rebuild")
		default:
		}
		if idx := p.GetIndex(); idx != nil {
			if len(idx.LevelIdx["INFO"]) > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	idx := p.GetIndex()
	if idx == nil {
		t.Fatal("expected non-nil index after rebuild")
	}
	if len(idx.LevelIdx["INFO"]) == 0 {
		t.Fatal("expected INFO entries in level index")
	}
}

func TestAlertDeduplication(t *testing.T) {
	cfg := testConfig(t)
	ch := make(chan model.RawLog, 16)
	p, _, _ := newTestPipeline(t, cfg, ch)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	logJSON := []byte(`{"timestamp":"2026-02-22T10:30:00.123+08:00","level":"ERROR","service":"test-svc","message":"test error message"}`)

	// Send the same ERROR log twice.
	ch <- model.RawLog{Line: logJSON, Source: "test-source"}
	ch <- model.RawLog{Line: logJSON, Source: "test-source"}

	// Collect alert requests with a timeout.
	var alerts []model.AlertRequest
	timeout := time.After(2 * time.Second)
	for {
		select {
		case req := <-p.AlertRequestChan():
			alerts = append(alerts, req)
			// Give a little time for any second alert to arrive.
			time.Sleep(200 * time.Millisecond)
		case <-timeout:
			goto done
		}
	}
done:
	if len(alerts) != 1 {
		t.Fatalf("expected exactly 1 alert request, got %d", len(alerts))
	}
}

func TestShutdown(t *testing.T) {
	cfg := testConfig(t)
	ch := make(chan model.RawLog, 16)
	p, ring, wal := newTestPipeline(t, cfg, ch)

	ctx, cancel := context.WithCancel(context.Background())

	// Drain alerts.
	go func() {
		for range p.AlertRequestChan() {
		}
	}()

	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	logJSON := []byte(`{"timestamp":"2026-02-22T10:30:00.123+08:00","level":"INFO","service":"test-svc","message":"pre-shutdown log"}`)
	ch <- model.RawLog{Line: logJSON, Source: "test-source"}

	// Wait for it to be ingested.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for pre-shutdown log")
		default:
		}
		if ring.Len() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Send another log that will be drained during Shutdown.
	shutdownLog := []byte(`{"timestamp":"2026-02-22T10:31:00.123+08:00","level":"INFO","service":"test-svc","message":"shutdown drain log"}`)
	ch <- model.RawLog{Line: shutdownLog, Source: "test-source"}

	// Cancel context, close channel, wait for Run to exit — mirrors main.go shutdown.
	cancel()
	close(ch)
	<-done

	p.Shutdown()

	if ring.Len() < 2 {
		t.Fatalf("expected at least 2 entries in ring after shutdown drain, got %d", ring.Len())
	}
	if wal.EntryCount() < 2 {
		t.Fatalf("expected at least 2 WAL entries after shutdown, got %d", wal.EntryCount())
	}

	// Verify checkpoint was written.
	cp, err := storage.LoadCheckpoint(cfg.Storage.DataDir)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if cp.WalByteOffset <= 0 {
		t.Fatalf("expected positive WAL byte offset in checkpoint, got %d", cp.WalByteOffset)
	}
}
