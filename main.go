package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"html/template"

	"github.com/admin/argus/internal/alert"
	"github.com/admin/argus/internal/config"
	"github.com/admin/argus/internal/model"
	"github.com/admin/argus/internal/parser"
	"github.com/admin/argus/internal/pipeline"
	"github.com/admin/argus/internal/server"
	"github.com/admin/argus/internal/storage"
	"github.com/admin/argus/internal/tailer"
	"github.com/admin/argus/web"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	// 1. Load and validate config.
	cfg, err := config.Load(*configPath)
	if err != nil {
		logJSON("error", "failed to load config", "error", err.Error())
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		logJSON("error", "config validation failed", "error", err.Error())
		os.Exit(1)
	}

	// 2. Load checkpoint.
	cp, err := storage.LoadCheckpoint(cfg.Storage.DataDir)
	if err != nil {
		logJSON("error", "failed to load checkpoint", "error", err.Error())
		os.Exit(1)
	}

	// 3. Create WAL, replay up to wal_byte_offset, rebuild RingBuffer + index.
	wal, err := storage.NewWAL(cfg.Storage.DataDir)
	if err != nil {
		logJSON("error", "failed to create WAL", "error", err.Error())
		os.Exit(1)
	}

	ring := storage.NewRingBuffer(cfg.Storage.MaxEntries)

	replayCount, err := wal.Replay(cp.WalByteOffset, func(seqNum int, data []byte) error {
		source, payload := storage.DecodeEnvelope(data)
		entry, err := parser.Parse(payload, source)
		if err != nil {
			return nil // skip unparseable entries
		}
		ring.Write(entry)
		return nil
	})
	if err != nil {
		logJSON("error", "WAL replay failed", "error", err.Error())
		os.Exit(1)
	}

	// Build initial index from replayed data.
	snapshot, tailSeq := ring.SnapshotLite()
	initialIndex := storage.BuildIndex(snapshot, tailSeq)

	// 4. Restore FingerprintRing from checkpoint.
	fpRing := storage.NewFingerprintRing(10000)
	fpRing.Import(cp.AlertFingerprints)

	// 5. Create shared log channel.
	logChan := make(chan model.RawLog, 2048)

	// 6. Create Pipeline.
	p := pipeline.NewPipeline(cfg, ring, wal, fpRing, logChan)
	p.SeedIndex(initialIndex)

	// 7. Set up context with cancellation.
	ctx, cancel := context.WithCancel(context.Background())

	// 8. Start tailer manager (supports hot-reload of sources).
	absConfigPath, _ := filepath.Abs(*configPath)
	mgr := tailer.NewManager(absConfigPath, logChan, cp.Cursors)
	if err := mgr.Start(ctx, cfg.Sources); err != nil {
		logJSON("error", "failed to start tailer manager", "error", err.Error())
		os.Exit(1)
	}

	// 9. Create and start FeishuWorker.
	feishuWorker := alert.NewFeishuWorker(
		cfg.Alert.Feishu.WebhookURL,
		cfg.Alert.Feishu.Secret,
		cfg.Server.BaseURL,
		cfg.Alert.MaxRetries,
		p.AlertRequestChan(),
		p.AlertResultChan(),
	)
	var alertWg sync.WaitGroup
	alertWg.Add(1)
	alertCtx, alertCancel := context.WithCancel(context.Background())
	go func() {
		defer alertWg.Done()
		feishuWorker.Run(alertCtx)
	}()

	// 10. Start pipeline.
	var pipelineWg sync.WaitGroup
	pipelineWg.Add(1)
	go func() {
		defer pipelineWg.Done()
		p.Run(ctx)
	}()

	// 10.5. Periodically sync tailer cursors to pipeline for checkpointing.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for id, raw := range mgr.GetFileCursors() {
					p.UpdateCursor(id, raw)
				}
				for id, raw := range mgr.GetDockerCursors() {
					p.UpdateCursor(id, raw)
				}
			}
		}
	}()

	// 11. Create Auth, Handler, Router and start HTTP server.
	auth, err := server.NewAuth(cfg.Auth.Username, cfg.Auth.PasswordHash, cfg.Storage.DataDir, cfg.Auth.JWTSecret)
	if err != nil {
		logJSON("error", "failed to create auth", "error", err.Error())
		os.Exit(1)
	}
	tmpl, err := template.ParseFS(web.TemplateFS, "templates/*.html")
	if err != nil {
		logJSON("error", "failed to parse templates", "error", err.Error())
		os.Exit(1)
	}
	handler := server.NewHandler(p, auth, tmpl)
	staticSub, _ := fs.Sub(web.StaticFS, "static")
	router := server.NewRouter(handler, auth, http.FS(staticSub))

	ln, err := net.Listen("tcp", cfg.Server.Addr)
	if err != nil {
		logJSON("error", "failed to listen", "addr", cfg.Server.Addr, "error", err.Error())
		cancel()
		os.Exit(1)
	}

	httpServer := &http.Server{
		Handler: router,
	}
	go func() {
		if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			logJSON("error", "HTTP server error", "error", err.Error())
		}
	}()

	host := "localhost"
	port := cfg.Server.Addr
	if port == "" || port[0] == ':' {
		if port == "" {
			port = ":8080"
		}
		host = "localhost" + port
	} else {
		host = port
	}

	logJSON("info", fmt.Sprintf("argus started — admin: http://%s/admin", host),
		"addr", cfg.Server.Addr,
		"sources", fmt.Sprintf("%d", len(cfg.Sources)),
		"wal_replayed", fmt.Sprintf("%d", replayCount),
		"fingerprints_restored", fmt.Sprintf("%d", len(cp.AlertFingerprints)),
	)

	// 12. Wait for SIGTERM/SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh

	logJSON("info", "shutdown initiated")

	// Hard deadline: force exit after 10s regardless.
	go func() {
		time.Sleep(10 * time.Second)
		logJSON("warn", "shutdown deadline exceeded, forcing exit")
		os.Exit(1)
	}()

	// Graceful shutdown sequence (design section 10):

	// Step 1: Cancel context & alert worker.
	cancel()
	alertCancel()

	// Step 2: Stop tailer manager (cancels all tailers, waits with timeout).
	mgr.Stop()

	// Step 3: Close logChan — safe now that tailers have stopped.
	close(logChan)

	// Step 4: Wait for pipeline run loop to exit (drains remaining logChan then returns).
	waitWithTimeout(&pipelineWg, 5*time.Second)

	// Step 5: Wait for alert worker (timeout 3s).
	waitWithTimeout(&alertWg, 3*time.Second)

	// Step 6: Pipeline.Shutdown() — fsync WAL, write final checkpoint.
	p.Shutdown()

	// Step 7: Shutdown HTTP server (timeout 5s).
	httpCtx, httpCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer httpCancel()
	if err := httpServer.Shutdown(httpCtx); err != nil {
		logJSON("error", "HTTP server shutdown error", "error", err.Error())
	}

	logJSON("info", "shutdown complete")
}

// waitWithTimeout waits for a WaitGroup with a timeout. Returns true if
// the WaitGroup completed before the timeout.
func waitWithTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// logJSON writes a structured JSON log line to stderr matching the standard
// schema with service: "argus".
func logJSON(level, msg string, kvs ...string) {
	m := map[string]string{
		"timestamp": time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02T15:04:05.000-07:00"),
		"level":     strings.ToUpper(level),
		"service":   "argus",
		"message":   msg,
	}
	for i := 0; i+1 < len(kvs); i += 2 {
		m[kvs[i]] = kvs[i+1]
	}
	data, _ := json.Marshal(m)
	fmt.Fprintln(os.Stderr, string(data))
}
