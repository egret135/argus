package storage

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
)

const (
	walFileName   = "wal.dat"
	walNewName    = "wal.new"
	walHeaderSize = 8 // 4 bytes length + 4 bytes CRC32
	walSepSize    = 1 // trailing \n
)

// WAL implements a write-ahead log with CRC32-verified records.
//
// Record format (total N+9 bytes):
//
//	[0:4]   uint32 LE  – JSON payload length N
//	[4:8]   uint32 LE  – CRC32 IEEE of JSON payload
//	[8:8+N] bytes      – JSON payload
//	[8+N]   byte       – '\n' record separator
type WAL struct {
	file        *os.File
	dataDir     string
	entryCount  int
	offsetIndex []int64
	byteOffset  int64
}

// NewWAL opens or creates the WAL file in dataDir.
func NewWAL(dataDir string) (*WAL, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("wal: create data dir: %w", err)
	}

	path := filepath.Join(dataDir, walFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("wal: open %s: %w", path, err)
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("wal: stat %s: %w", path, err)
	}

	w := &WAL{
		file:       f,
		dataDir:    dataDir,
		byteOffset: info.Size(),
	}
	return w, nil
}

// Append writes a single record to the WAL.
func (w *WAL) Append(payload []byte) error {
	n := uint32(len(payload))
	checksum := crc32.ChecksumIEEE(payload)

	var header [walHeaderSize]byte
	binary.LittleEndian.PutUint32(header[0:4], n)
	binary.LittleEndian.PutUint32(header[4:8], checksum)

	recordStart := w.byteOffset

	// Write header.
	if _, err := w.file.Write(header[:]); err != nil {
		return fmt.Errorf("wal: write header: %w", err)
	}
	// Write payload.
	if _, err := w.file.Write(payload); err != nil {
		return fmt.Errorf("wal: write payload: %w", err)
	}
	// Write record separator.
	if _, err := w.file.Write([]byte{'\n'}); err != nil {
		return fmt.Errorf("wal: write separator: %w", err)
	}

	w.offsetIndex = append(w.offsetIndex, recordStart)
	w.byteOffset += int64(walHeaderSize) + int64(n) + walSepSize
	w.entryCount++
	return nil
}

// Sync flushes the WAL file to stable storage (fsync).
func (w *WAL) Sync() error {
	return w.file.Sync()
}

// ByteOffset returns the current write position (end of file).
func (w *WAL) ByteOffset() int64 {
	return w.byteOffset
}

// EntryCount returns the number of records tracked in memory.
func (w *WAL) EntryCount() int {
	return w.entryCount
}

// Replay reads the WAL from the beginning, verifies CRC32 per record, and
// calls fn for each valid record. If upToOffset is 0, all records are
// replayed; otherwise only records whose start offset < upToOffset are
// replayed.
//
// On CRC failure or incomplete record the file is truncated to the last
// valid record boundary and a warning is printed to stderr.
//
// Returns the number of valid entries replayed.
func (w *WAL) Replay(upToOffset int64, fn func(seqNum int, payload []byte) error) (int, error) {
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("wal: seek to start: %w", err)
	}

	var (
		offset      int64
		count       int
		offsetIndex []int64
		header      [walHeaderSize]byte
	)

	for {
		// Stop if we've reached the requested boundary.
		if upToOffset > 0 && offset >= upToOffset {
			break
		}

		recordStart := offset

		// Read header.
		if _, err := io.ReadFull(w.file, header[:]); err != nil {
			if err == io.EOF {
				break // clean end of file
			}
			// Incomplete header — truncate.
			fmt.Fprintf(os.Stderr, "WARNING: wal: incomplete header at offset %d, truncating\n", offset)
			if tErr := w.truncate(offset, count, offsetIndex); tErr != nil {
				return count, tErr
			}
			break
		}

		n := binary.LittleEndian.Uint32(header[0:4])
		expectedCRC := binary.LittleEndian.Uint32(header[4:8])

		// Read payload + separator.
		recordBody := make([]byte, int(n)+walSepSize)
		if _, err := io.ReadFull(w.file, recordBody); err != nil {
			fmt.Fprintf(os.Stderr, "WARNING: wal: incomplete record at offset %d, truncating\n", offset)
			if tErr := w.truncate(offset, count, offsetIndex); tErr != nil {
				return count, tErr
			}
			break
		}

		payload := recordBody[:n]
		actualCRC := crc32.ChecksumIEEE(payload)
		if actualCRC != expectedCRC {
			fmt.Fprintf(os.Stderr, "WARNING: wal: CRC32 mismatch at offset %d (expected %08x, got %08x), truncating\n",
				offset, expectedCRC, actualCRC)
			if tErr := w.truncate(offset, count, offsetIndex); tErr != nil {
				return count, tErr
			}
			break
		}

		offsetIndex = append(offsetIndex, recordStart)
		offset += int64(walHeaderSize) + int64(n) + walSepSize

		if err := fn(count, payload); err != nil {
			return count, fmt.Errorf("wal: replay callback: %w", err)
		}
		count++
	}

	w.entryCount = count
	w.offsetIndex = offsetIndex
	w.byteOffset = offset

	return count, nil
}

// truncate cuts the WAL file to the given valid offset and updates internal
// state.
func (w *WAL) truncate(validOffset int64, count int, offsetIndex []int64) error {
	if err := w.file.Truncate(validOffset); err != nil {
		return fmt.Errorf("wal: truncate to %d: %w", validOffset, err)
	}
	if _, err := w.file.Seek(validOffset, io.SeekStart); err != nil {
		return fmt.Errorf("wal: seek after truncate: %w", err)
	}
	w.byteOffset = validOffset
	w.entryCount = count
	w.offsetIndex = offsetIndex
	return nil
}

// Compact keeps only the last keepLastN records. It copies those records to a
// new file, replaces the old WAL, and reopens it.
func (w *WAL) Compact(keepLastN int) error {
	total := len(w.offsetIndex)
	if keepLastN >= total {
		return nil // nothing to compact
	}

	startIdx := total - keepLastN
	newPath := filepath.Join(w.dataDir, walNewName)

	nf, err := os.OpenFile(newPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("wal: create %s: %w", newPath, err)
	}

	// Copy selected records as raw bytes.
	for i := startIdx; i < total; i++ {
		recOff := w.offsetIndex[i]

		// Determine record size.
		var recEnd int64
		if i+1 < total {
			recEnd = w.offsetIndex[i+1]
		} else {
			recEnd = w.byteOffset
		}
		recSize := recEnd - recOff

		if _, err := w.file.Seek(recOff, io.SeekStart); err != nil {
			nf.Close()
			os.Remove(newPath)
			return fmt.Errorf("wal: seek to record %d: %w", i, err)
		}

		if _, err := io.CopyN(nf, w.file, recSize); err != nil {
			nf.Close()
			os.Remove(newPath)
			return fmt.Errorf("wal: copy record %d: %w", i, err)
		}
	}

	if err := nf.Sync(); err != nil {
		nf.Close()
		os.Remove(newPath)
		return fmt.Errorf("wal: fsync new file: %w", err)
	}

	// Close old file.
	if err := w.file.Close(); err != nil {
		nf.Close()
		os.Remove(newPath)
		return fmt.Errorf("wal: close old file: %w", err)
	}

	walPath := filepath.Join(w.dataDir, walFileName)
	if err := os.Rename(newPath, walPath); err != nil {
		// Try to reopen old file to avoid leaving WAL in a broken state.
		w.file, _ = os.OpenFile(walPath, os.O_CREATE|os.O_RDWR, 0o644)
		nf.Close()
		return fmt.Errorf("wal: rename %s -> %s: %w", newPath, walPath, err)
	}

	// Reopen the new file as wal.dat.
	nf.Close()
	f, err := os.OpenFile(walPath, os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("wal: reopen %s: %w", walPath, err)
	}
	w.file = f

	// Rebuild offset index.
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("wal: stat after compact: %w", err)
	}

	w.byteOffset = info.Size()
	w.entryCount = 0
	w.offsetIndex = nil

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("wal: seek for rebuild: %w", err)
	}

	var (
		offset int64
		header [walHeaderSize]byte
	)
	for {
		if _, err := io.ReadFull(f, header[:]); err != nil {
			break
		}
		n := binary.LittleEndian.Uint32(header[0:4])
		w.offsetIndex = append(w.offsetIndex, offset)
		recordSize := int64(walHeaderSize) + int64(n) + walSepSize
		offset += recordSize
		w.entryCount++

		// Skip past payload + separator.
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			break
		}
	}

	// Seek to end for subsequent appends.
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("wal: seek to end after compact: %w", err)
	}

	return nil
}

// WALEnvelope wraps a raw log line with its source for WAL storage.
type WALEnvelope struct {
	Source  string          `json:"_src"`
	Payload json.RawMessage `json:"_payload"`
}

// EncodeEnvelope wraps a raw log line and source into a WAL envelope.
func EncodeEnvelope(payload []byte, source string) []byte {
	env := WALEnvelope{Source: source, Payload: payload}
	data, _ := json.Marshal(env)
	return data
}

// DecodeEnvelope extracts the source and original payload from a WAL envelope.
// If the data is not an envelope (legacy format), it returns empty source and
// the original data as payload.
func DecodeEnvelope(data []byte) (source string, payload []byte) {
	var env WALEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return "", data
	}
	if env.Payload == nil {
		return "", data
	}
	return env.Source, env.Payload
}

// Close closes the WAL file.
func (w *WAL) Close() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
