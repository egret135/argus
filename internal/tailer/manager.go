package tailer

import (
	"context"
	"encoding/json"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/admin/argus/internal/config"
	"github.com/admin/argus/internal/model"
	"github.com/admin/argus/internal/storage"
	"github.com/fsnotify/fsnotify"
)

// ManagedTailer wraps a running tailer with its cancellation and wait group.
type ManagedTailer struct {
	sourceID     string
	srcType      string // "file" or "docker"
	fileTailer   *FileTailer
	dockerTailer *DockerTailer
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

// Manager owns all file and docker tailers and supports hot-reloading
// sources from config.yaml via fsnotify.
type Manager struct {
	configPath        string
	logChan           chan<- model.RawLog
	tailers           map[string]*ManagedTailer
	mu                sync.Mutex
	ctx               context.Context
	cancel            context.CancelFunc
	checkpointCursors map[string]json.RawMessage
}

// NewManager creates a Manager that sends log lines to logChan and uses
// checkpointCursors to restore cursor positions for tailers.
func NewManager(configPath string, logChan chan<- model.RawLog, checkpointCursors map[string]json.RawMessage) *Manager {
	return &Manager{
		configPath:        configPath,
		logChan:           logChan,
		tailers:           make(map[string]*ManagedTailer),
		checkpointCursors: checkpointCursors,
	}
}

// Start starts initial tailers from sources and begins watching the config
// file for changes.
func (m *Manager) Start(ctx context.Context, sources []config.SourceConfig) error {
	m.ctx, m.cancel = context.WithCancel(ctx)

	m.mu.Lock()
	for _, src := range sources {
		m.startTailerLocked(src)
	}
	m.mu.Unlock()

	go m.watchConfig()

	return nil
}

// Stop gracefully stops all tailers and the config watcher.
func (m *Manager) Stop() {
	m.cancel()

	m.mu.Lock()
	defer m.mu.Unlock()

	for id, mt := range m.tailers {
		mt.cancel()
		done := make(chan struct{})
		go func() {
			mt.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			log.Printf("tailer: manager: timeout waiting for %s to stop", id)
		}
		delete(m.tailers, id)
	}
}

// GetFileCursors returns current cursors for all active file tailers.
func (m *Manager) GetFileCursors() map[string]json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()

	cursors := make(map[string]json.RawMessage)
	for id, mt := range m.tailers {
		if mt.srcType == "file" && mt.fileTailer != nil {
			cursors[id] = storage.MarshalFileCursor(mt.fileTailer.GetCursor())
		}
	}
	return cursors
}

// GetDockerCursors returns current cursors for all active docker tailers.
func (m *Manager) GetDockerCursors() map[string]json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()

	cursors := make(map[string]json.RawMessage)
	for id, mt := range m.tailers {
		if mt.srcType == "docker" && mt.dockerTailer != nil {
			cursors[id] = storage.MarshalDockerCursor(mt.dockerTailer.GetCursor())
		}
	}
	return cursors
}

// sourceID returns the unique identifier for a source config.
func sourceID(src config.SourceConfig) string {
	switch src.Type {
	case "file":
		return "file:" + src.Path
	case "docker":
		return "docker:" + src.Container
	default:
		return src.Type + ":" + src.Path + src.Container
	}
}

// startTailerLocked starts a tailer for the given source. Must be called
// with m.mu held.
func (m *Manager) startTailerLocked(src config.SourceConfig) {
	id := sourceID(src)
	if _, exists := m.tailers[id]; exists {
		return
	}

	childCtx, childCancel := context.WithCancel(m.ctx)
	mt := &ManagedTailer{
		sourceID: id,
		srcType:  src.Type,
		cancel:   childCancel,
	}

	switch src.Type {
	case "file":
		var cursor storage.FileCursor
		if raw, ok := m.checkpointCursors[id]; ok {
			cursor, _ = storage.ParseFileCursor(raw)
		}
		ft := NewFileTailer(src.Path, m.logChan, cursor)
		mt.fileTailer = ft
		mt.wg.Add(1)
		go func() {
			defer mt.wg.Done()
			ft.Run(childCtx)
		}()

	case "docker":
		var cursor storage.DockerCursor
		if raw, ok := m.checkpointCursors[id]; ok {
			cursor, _ = storage.ParseDockerCursor(raw)
		}
		dt := NewDockerTailer(src.Container, m.logChan, cursor)
		mt.dockerTailer = dt
		mt.wg.Add(1)
		go func() {
			defer mt.wg.Done()
			dt.Run(childCtx)
		}()
	}

	m.tailers[id] = mt
}

// stopTailerLocked stops and removes a managed tailer. Must be called
// with m.mu held.
func (m *Manager) stopTailerLocked(id string) {
	mt, ok := m.tailers[id]
	if !ok {
		return
	}

	mt.cancel()
	delete(m.tailers, id)

	// Wait in a goroutine-safe manner with timeout, but don't hold the lock
	// during the wait. The tailer is already removed from the map.
	go func() {
		done := make(chan struct{})
		go func() {
			mt.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			log.Printf("tailer: manager: timeout waiting for %s to stop", id)
		}
	}()
}

// watchConfig watches the config file's parent directory for changes and
// reloads sources on write or create events.
func (m *Manager) watchConfig() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("tailer: manager: fsnotify: %v", err)
		return
	}
	defer watcher.Close()

	configDir := filepath.Dir(m.configPath)
	configBase := filepath.Base(m.configPath)

	if err := watcher.Add(configDir); err != nil {
		log.Printf("tailer: manager: watch %s: %v", configDir, err)
		return
	}

	var debounceTimer *time.Timer

	for {
		select {
		case <-m.ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Base(event.Name) != configBase {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(500*time.Millisecond, func() {
				m.reloadSources()
			})

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("tailer: manager: watcher error: %v", err)
		}
	}
}

// reloadSources re-reads the config file and diffs the sources against
// the currently running tailers.
func (m *Manager) reloadSources() {
	cfg, err := config.Load(m.configPath)
	if err != nil {
		log.Printf("tailer: manager: reload config: %v", err)
		return
	}

	if err := cfg.Validate(); err != nil {
		log.Printf("tailer: manager: reload config validation: %v", err)
		return
	}

	desired := make(map[string]config.SourceConfig, len(cfg.Sources))
	for _, src := range cfg.Sources {
		desired[sourceID(src)] = src
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Find removed tailers.
	var removed []string
	for id := range m.tailers {
		if _, ok := desired[id]; !ok {
			removed = append(removed, id)
		}
	}

	// Stop removed tailers.
	for _, id := range removed {
		m.stopTailerLocked(id)
	}

	// Start new tailers.
	var added int
	for id, src := range desired {
		if _, exists := m.tailers[id]; !exists {
			m.startTailerLocked(src)
			added++
		}
	}

	log.Printf("tailer: manager: config reloaded: added %d, removed %d sources", added, len(removed))
}
