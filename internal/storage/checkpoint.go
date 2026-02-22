package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	checkpointFile = "checkpoint.json"
	checkpointTmp  = "checkpoint.tmp"
)

// Checkpoint holds persisted cursor positions, WAL offset, and alert fingerprints.
type Checkpoint struct {
	Cursors           map[string]json.RawMessage `json:"cursors"`
	WalByteOffset     int64                      `json:"wal_byte_offset"`
	AlertFingerprints []string                   `json:"alert_fingerprints"`
}

// FileCursor tracks the read position for a file-based log source.
type FileCursor struct {
	Inode    uint64 `json:"inode"`
	Dev      uint64 `json:"dev"`
	Offset   int64  `json:"offset"`
	FileSize int64  `json:"file_size"`
	Gen      int    `json:"gen"`
}

// DockerCursor tracks the read position for a Docker container log source.
type DockerCursor struct {
	LastTS       string   `json:"last_ts"`
	LastTSHashes []string `json:"last_ts_hashes"`
}

// LoadCheckpoint reads checkpoint.json from dataDir. If the file does not
// exist, a zero-value Checkpoint is returned.
func LoadCheckpoint(dataDir string) (*Checkpoint, error) {
	path := filepath.Join(dataDir, checkpointFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Checkpoint{Cursors: make(map[string]json.RawMessage)}, nil
		}
		return nil, fmt.Errorf("checkpoint: read %s: %w", path, err)
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("checkpoint: unmarshal %s: %w", path, err)
	}
	if cp.Cursors == nil {
		cp.Cursors = make(map[string]json.RawMessage)
	}
	return &cp, nil
}

// SaveCheckpoint atomically writes the checkpoint to dataDir by writing to a
// temporary file first, then renaming it to checkpoint.json.
func SaveCheckpoint(dataDir string, cp *Checkpoint) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("checkpoint: create data dir: %w", err)
	}

	data, err := json.Marshal(cp)
	if err != nil {
		return fmt.Errorf("checkpoint: marshal: %w", err)
	}

	tmpPath := filepath.Join(dataDir, checkpointTmp)
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("checkpoint: write %s: %w", tmpPath, err)
	}

	finalPath := filepath.Join(dataDir, checkpointFile)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("checkpoint: rename %s -> %s: %w", tmpPath, finalPath, err)
	}
	return nil
}

// ParseFileCursor unmarshals a FileCursor from a raw JSON cursor value.
func ParseFileCursor(raw json.RawMessage) (FileCursor, error) {
	var fc FileCursor
	if err := json.Unmarshal(raw, &fc); err != nil {
		return FileCursor{}, fmt.Errorf("checkpoint: parse file cursor: %w", err)
	}
	return fc, nil
}

// ParseDockerCursor unmarshals a DockerCursor from a raw JSON cursor value.
func ParseDockerCursor(raw json.RawMessage) (DockerCursor, error) {
	var dc DockerCursor
	if err := json.Unmarshal(raw, &dc); err != nil {
		return DockerCursor{}, fmt.Errorf("checkpoint: parse docker cursor: %w", err)
	}
	return dc, nil
}

// MarshalFileCursor serializes a FileCursor to json.RawMessage.
func MarshalFileCursor(fc FileCursor) json.RawMessage {
	data, _ := json.Marshal(fc)
	return data
}

// MarshalDockerCursor serializes a DockerCursor to json.RawMessage.
func MarshalDockerCursor(dc DockerCursor) json.RawMessage {
	data, _ := json.Marshal(dc)
	return data
}

// FingerprintRing is a fixed-capacity FIFO set for alert fingerprints.
// When the ring is full, adding a new fingerprint evicts the oldest one.
type FingerprintRing struct {
	fpMap  map[string]struct{}
	fpRing []string
	fpPos  int
}

// NewFingerprintRing creates a FingerprintRing with the given capacity.
func NewFingerprintRing(capacity int) *FingerprintRing {
	return &FingerprintRing{
		fpMap:  make(map[string]struct{}, capacity),
		fpRing: make([]string, capacity),
	}
}

// Contains reports whether fp is in the ring.
func (r *FingerprintRing) Contains(fp string) bool {
	_, ok := r.fpMap[fp]
	return ok
}

// Add inserts fp into the ring. If fp already exists, it is a no-op.
// If the ring is full, the oldest fingerprint is evicted.
func (r *FingerprintRing) Add(fp string) {
	if _, ok := r.fpMap[fp]; ok {
		return
	}

	// Evict the entry at the current position if occupied.
	if old := r.fpRing[r.fpPos]; old != "" {
		delete(r.fpMap, old)
	}

	r.fpRing[r.fpPos] = fp
	r.fpMap[fp] = struct{}{}
	r.fpPos = (r.fpPos + 1) % len(r.fpRing)
}

// Export returns all fingerprints in insertion order (oldest first) for
// serialization into a checkpoint.
func (r *FingerprintRing) Export() []string {
	out := make([]string, 0, len(r.fpMap))
	for i := 0; i < len(r.fpRing); i++ {
		idx := (r.fpPos + i) % len(r.fpRing)
		if r.fpRing[idx] != "" {
			out = append(out, r.fpRing[idx])
		}
	}
	return out
}

// Import restores the ring from a checkpoint by adding each fingerprint in
// order.
func (r *FingerprintRing) Import(fps []string) {
	for _, fp := range fps {
		r.Add(fp)
	}
}
