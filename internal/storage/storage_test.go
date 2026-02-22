package storage

import (
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/admin/argus/internal/model"
)

// ---------------------------------------------------------------------------
// WAL
// ---------------------------------------------------------------------------

func TestWAL_AppendAndReplay(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer w.Close()

	payloads := []string{`{"msg":"one"}`, `{"msg":"two"}`, `{"msg":"three"}`}
	for _, p := range payloads {
		if err := w.Append([]byte(p)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	if w.EntryCount() != 3 {
		t.Fatalf("EntryCount = %d, want 3", w.EntryCount())
	}

	var replayed []string
	n, err := w.Replay(0, func(seqNum int, payload []byte) error {
		replayed = append(replayed, string(payload))
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if n != 3 {
		t.Fatalf("Replay count = %d, want 3", n)
	}
	for i, want := range payloads {
		if replayed[i] != want {
			t.Errorf("replayed[%d] = %q, want %q", i, replayed[i], want)
		}
	}
}

func TestWAL_CRCCorruption(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}

	for i := 0; i < 5; i++ {
		if err := w.Append([]byte(`{"n":` + string(rune('0'+i)) + `}`)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	w.Close()

	// Corrupt the CRC of the 3rd record (index 2).
	walPath := filepath.Join(dir, "wal.dat")
	data, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Walk to the 3rd record and flip a CRC byte.
	offset := 0
	for i := 0; i < 2; i++ {
		n := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
		offset += walHeaderSize + n + walSepSize
	}
	// offset now points to record #2; CRC is at offset+4..offset+8.
	data[offset+4] ^= 0xFF
	if err := os.WriteFile(walPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Replay should return only the 2 valid records before corruption.
	w2, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL after corrupt: %v", err)
	}
	defer w2.Close()

	n, err := w2.Replay(0, func(_ int, _ []byte) error { return nil })
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if n != 2 {
		t.Fatalf("Replay count = %d, want 2 (truncated at corruption)", n)
	}

	// File should have been truncated — new appends should still work.
	if err := w2.Append([]byte(`{"after":"corrupt"}`)); err != nil {
		t.Fatalf("Append after corruption: %v", err)
	}
	if w2.EntryCount() != 3 {
		t.Fatalf("EntryCount after re-append = %d, want 3", w2.EntryCount())
	}
}

func TestWAL_Compact(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer w.Close()

	payloads := []string{"aaa", "bbb", "ccc", "ddd", "eee"}
	for _, p := range payloads {
		if err := w.Append([]byte(p)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// Keep last 2 records.
	if err := w.Compact(2); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if w.EntryCount() != 2 {
		t.Fatalf("EntryCount after Compact = %d, want 2", w.EntryCount())
	}

	// Verify content via Replay.
	var replayed []string
	n, err := w.Replay(0, func(_ int, payload []byte) error {
		replayed = append(replayed, string(payload))
		return nil
	})
	if err != nil {
		t.Fatalf("Replay after Compact: %v", err)
	}
	if n != 2 {
		t.Fatalf("Replay count = %d, want 2", n)
	}
	want := []string{"ddd", "eee"}
	if !reflect.DeepEqual(replayed, want) {
		t.Fatalf("replayed = %v, want %v", replayed, want)
	}

	// Verify CRCs are valid by re-reading the raw file.
	walPath := filepath.Join(dir, "wal.dat")
	raw, _ := os.ReadFile(walPath)
	off := 0
	for off < len(raw) {
		pLen := int(binary.LittleEndian.Uint32(raw[off : off+4]))
		expectedCRC := binary.LittleEndian.Uint32(raw[off+4 : off+8])
		payload := raw[off+8 : off+8+pLen]
		actualCRC := crc32.ChecksumIEEE(payload)
		if actualCRC != expectedCRC {
			t.Fatalf("CRC mismatch after compact at offset %d", off)
		}
		off += walHeaderSize + pLen + walSepSize
	}
}

// ---------------------------------------------------------------------------
// RingBuffer
// ---------------------------------------------------------------------------

func TestRingBuffer_BasicWriteRead(t *testing.T) {
	rb := NewRingBuffer(16)

	entries := []model.LogEntry{
		{Message: "hello", Level: "info"},
		{Message: "world", Level: "warn"},
	}
	for _, e := range entries {
		rb.Write(e)
	}

	if rb.Len() != 2 {
		t.Fatalf("Len = %d, want 2", rb.Len())
	}

	e0, ok := rb.Read(0)
	if !ok {
		t.Fatal("Read(0) returned false")
	}
	if e0.Message != "hello" {
		t.Errorf("Read(0).Message = %q, want %q", e0.Message, "hello")
	}
	if e0.SeqID != 0 {
		t.Errorf("Read(0).SeqID = %d, want 0", e0.SeqID)
	}

	e1, ok := rb.Read(1)
	if !ok {
		t.Fatal("Read(1) returned false")
	}
	if e1.Message != "world" {
		t.Errorf("Read(1).Message = %q, want %q", e1.Message, "world")
	}

	// Out of range.
	_, ok = rb.Read(2)
	if ok {
		t.Error("Read(2) should return false")
	}

	// Range
	rangeResult := rb.Range(0, 2)
	if len(rangeResult) != 2 {
		t.Fatalf("Range(0,2) len = %d, want 2", len(rangeResult))
	}
}

func TestRingBuffer_Overflow(t *testing.T) {
	cap := 4
	rb := NewRingBuffer(cap)

	// Write more entries than capacity.
	for i := 0; i < 10; i++ {
		rb.Write(model.LogEntry{Message: string(rune('A' + i))})
	}

	if rb.Len() != cap {
		t.Fatalf("Len = %d, want %d", rb.Len(), cap)
	}
	if rb.Head() != 6 {
		t.Fatalf("Head = %d, want 6", rb.Head())
	}
	if rb.Tail() != 10 {
		t.Fatalf("Tail = %d, want 10", rb.Tail())
	}

	// Oldest entry (seqID 0) should be evicted.
	_, ok := rb.Read(0)
	if ok {
		t.Error("Read(0) should return false after overflow")
	}

	// Latest entries should be readable.
	e, ok := rb.Read(9)
	if !ok {
		t.Fatal("Read(9) returned false")
	}
	if e.SeqID != 9 {
		t.Errorf("SeqID = %d, want 9", e.SeqID)
	}
}

func TestRingBuffer_SnapshotLite(t *testing.T) {
	rb := NewRingBuffer(8)
	for i := 0; i < 5; i++ {
		rb.Write(model.LogEntry{
			Message: "msg",
			Level:   "info",
			Source:  "src",
		})
	}

	snap, tail := rb.SnapshotLite()
	if tail != 5 {
		t.Fatalf("tail = %d, want 5", tail)
	}
	if len(snap) != 5 {
		t.Fatalf("snap len = %d, want 5", len(snap))
	}

	for i, e := range snap {
		if e.SeqID != uint64(i) {
			t.Errorf("snap[%d].SeqID = %d, want %d", i, e.SeqID, i)
		}
		if e.Level != "info" {
			t.Errorf("snap[%d].Level = %q, want %q", i, e.Level, "info")
		}
	}
}

// ---------------------------------------------------------------------------
// Inverted Index
// ---------------------------------------------------------------------------

func TestBuildIndex_SearchWithFilters(t *testing.T) {
	entries := []model.EntryLite{
		{SeqID: 0, Message: "connection timeout error", Level: "error", Source: "nginx"},
		{SeqID: 1, Message: "connection established", Level: "info", Source: "nginx"},
		{SeqID: 2, Message: "disk usage warning", Level: "warn", Source: "syslog"},
		{SeqID: 3, Message: "connection reset error", Level: "error", Source: "app"},
	}

	idx := BuildIndex(entries, 4)

	// Query only.
	got := idx.Search("connection", "", "")
	want := []uint64{0, 1, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Search(connection) = %v, want %v", got, want)
	}

	// Query + level filter.
	got = idx.Search("connection", "error", "")
	want = []uint64{0, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Search(connection, error) = %v, want %v", got, want)
	}

	// Query + level + source filter.
	got = idx.Search("connection", "error", "nginx")
	want = []uint64{0}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Search(connection, error, nginx) = %v, want %v", got, want)
	}

	// Level filter only.
	got = idx.Search("", "warn", "")
	want = []uint64{2}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Search('', warn) = %v, want %v", got, want)
	}

	// Source filter only.
	got = idx.Search("", "", "nginx")
	want = []uint64{0, 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Search('', '', nginx) = %v, want %v", got, want)
	}

	// Multi-token query (intersection).
	got = idx.Search("connection error", "", "")
	want = []uint64{0, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Search(connection error) = %v, want %v", got, want)
	}

	// No match.
	got = idx.Search("nonexistent", "", "")
	if len(got) != 0 {
		t.Errorf("Search(nonexistent) = %v, want empty", got)
	}
}

func TestBuildIndex_EmptyQuery(t *testing.T) {
	entries := []model.EntryLite{
		{SeqID: 0, Message: "hello world", Level: "info", Source: "app"},
	}
	idx := BuildIndex(entries, 1)

	// All filters empty — should return nil (no filtering applied).
	got := idx.Search("", "", "")
	if got != nil {
		t.Errorf("Search('','','') = %v, want nil", got)
	}

	// Query composed entirely of stop words — tokenize returns empty.
	got = idx.Search("the a an is", "", "")
	if got != nil {
		t.Errorf("Search(stop words) = %v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// Checkpoint
// ---------------------------------------------------------------------------

func TestCheckpoint_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Loading a non-existent checkpoint should return zero-value.
	cp, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint (empty): %v", err)
	}
	if cp.WalByteOffset != 0 {
		t.Fatalf("WalByteOffset = %d, want 0", cp.WalByteOffset)
	}

	// Build a checkpoint with cursor data and fingerprints.
	fc := FileCursor{Inode: 12345, Dev: 1, Offset: 4096, FileSize: 8192, Gen: 2}
	cp.Cursors["myfile"] = MarshalFileCursor(fc)
	cp.WalByteOffset = 9999
	cp.AlertFingerprints = []string{"fp-aaa", "fp-bbb"}

	if err := SaveCheckpoint(dir, cp); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	// Reload and verify.
	cp2, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if cp2.WalByteOffset != 9999 {
		t.Errorf("WalByteOffset = %d, want 9999", cp2.WalByteOffset)
	}
	if len(cp2.AlertFingerprints) != 2 || cp2.AlertFingerprints[0] != "fp-aaa" {
		t.Errorf("AlertFingerprints = %v, want [fp-aaa fp-bbb]", cp2.AlertFingerprints)
	}

	// Parse cursor back.
	raw, ok := cp2.Cursors["myfile"]
	if !ok {
		t.Fatal("cursor 'myfile' not found")
	}
	fc2, err := ParseFileCursor(raw)
	if err != nil {
		t.Fatalf("ParseFileCursor: %v", err)
	}
	if fc2 != fc {
		t.Errorf("FileCursor = %+v, want %+v", fc2, fc)
	}
}

func TestCheckpoint_DockerCursor(t *testing.T) {
	dc := DockerCursor{LastTS: "2026-01-01T00:00:00Z", LastTSHashes: []string{"h1", "h2"}}
	raw := MarshalDockerCursor(dc)

	dc2, err := ParseDockerCursor(raw)
	if err != nil {
		t.Fatalf("ParseDockerCursor: %v", err)
	}
	if dc2.LastTS != dc.LastTS {
		t.Errorf("LastTS = %q, want %q", dc2.LastTS, dc.LastTS)
	}
	if !reflect.DeepEqual(dc2.LastTSHashes, dc.LastTSHashes) {
		t.Errorf("LastTSHashes = %v, want %v", dc2.LastTSHashes, dc.LastTSHashes)
	}
}

func TestCheckpoint_FingerprintRingInCheckpoint(t *testing.T) {
	dir := t.TempDir()

	ring := NewFingerprintRing(4)
	ring.Add("a")
	ring.Add("b")
	ring.Add("c")

	cp := &Checkpoint{
		Cursors:           make(map[string]json.RawMessage),
		AlertFingerprints: ring.Export(),
	}
	if err := SaveCheckpoint(dir, cp); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	cp2, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}

	ring2 := NewFingerprintRing(4)
	ring2.Import(cp2.AlertFingerprints)

	for _, fp := range []string{"a", "b", "c"} {
		if !ring2.Contains(fp) {
			t.Errorf("ring2 should contain %q after import", fp)
		}
	}
}

// ---------------------------------------------------------------------------
// FingerprintRing
// ---------------------------------------------------------------------------

func TestFingerprintRing_Eviction(t *testing.T) {
	ring := NewFingerprintRing(3)

	ring.Add("a")
	ring.Add("b")
	ring.Add("c")

	// Ring is now full. Adding "d" should evict "a".
	ring.Add("d")

	if ring.Contains("a") {
		t.Error("ring should NOT contain 'a' after eviction")
	}
	for _, fp := range []string{"b", "c", "d"} {
		if !ring.Contains(fp) {
			t.Errorf("ring should contain %q", fp)
		}
	}

	// Adding duplicate should be a no-op.
	ring.Add("d")
	if !ring.Contains("d") {
		t.Error("duplicate Add should keep 'd'")
	}
	// "b" should still be present (no eviction on duplicate).
	if !ring.Contains("b") {
		t.Error("'b' should still be present after duplicate Add")
	}
}

func TestFingerprintRing_ImportExport(t *testing.T) {
	ring := NewFingerprintRing(5)
	ring.Add("x")
	ring.Add("y")
	ring.Add("z")

	exported := ring.Export()
	want := []string{"x", "y", "z"}
	if !reflect.DeepEqual(exported, want) {
		t.Fatalf("Export = %v, want %v", exported, want)
	}

	ring2 := NewFingerprintRing(5)
	ring2.Import(exported)

	for _, fp := range want {
		if !ring2.Contains(fp) {
			t.Errorf("ring2 should contain %q", fp)
		}
	}

	// Export order should match after import.
	exported2 := ring2.Export()
	if !reflect.DeepEqual(exported2, want) {
		t.Errorf("Export after Import = %v, want %v", exported2, want)
	}
}

func TestFingerprintRing_CapacityAndOrder(t *testing.T) {
	ring := NewFingerprintRing(3)
	// Fill and overflow.
	ring.Add("a")
	ring.Add("b")
	ring.Add("c")
	ring.Add("d")
	ring.Add("e")

	// Only the last 3 should remain.
	exported := ring.Export()
	want := []string{"c", "d", "e"}
	if !reflect.DeepEqual(exported, want) {
		t.Fatalf("Export = %v, want %v (oldest first)", exported, want)
	}
}
