package tailer

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/admin/argus/internal/model"
	"github.com/admin/argus/internal/storage"
	"github.com/fsnotify/fsnotify"
)

const (
	filePollInterval     = 2 * time.Second
	rotateCheckInterval  = 5 * time.Second
	initialTailLines     = 5000
	estimatedLineSize    = 512 // bytes per line estimate for seeking
)

// FileTailer follows a single log file, handling logrotate (rename and
// copytruncate) scenarios, and sends raw JSON lines to outChan.
type FileTailer struct {
	path    string
	outChan chan<- model.RawLog
	cursor  storage.FileCursor
	cursorMu sync.Mutex

	dropped atomic.Int64

	ctx context.Context
}

// NewFileTailer creates a FileTailer for the given path.
func NewFileTailer(path string, outChan chan<- model.RawLog, cursor storage.FileCursor) *FileTailer {
	return &FileTailer{
		path:    path,
		outChan: outChan,
		cursor:  cursor,
	}
}

// Source returns the source identifier for this tailer.
func (ft *FileTailer) Source() string {
	return "file:" + ft.path
}

// GetCursor returns the current cursor under lock.
func (ft *FileTailer) GetCursor() storage.FileCursor {
	ft.cursorMu.Lock()
	c := ft.cursor
	ft.cursorMu.Unlock()
	return c
}

// Run opens the file and tails it until ctx is cancelled.
func (ft *FileTailer) Run(ctx context.Context) {
	ft.ctx = ctx
	for {
		f, err := ft.openFile(ctx)
		if err != nil {
			return // context cancelled
		}

		ft.tailFile(ctx, f)
		f.Close()

		if ctx.Err() != nil {
			return
		}
	}
}

// openFile waits for the file to appear, then opens and seeks it.
func (ft *FileTailer) openFile(ctx context.Context) (*os.File, error) {
	for {
		f, err := os.Open(ft.path)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Printf("tailer: open %s: %v", ft.path, err)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(filePollInterval):
				continue
			}
		}

		info, err := f.Stat()
		if err != nil {
			f.Close()
			log.Printf("tailer: stat %s: %v", ft.path, err)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(filePollInterval):
				continue
			}
		}

		inode, dev := fileIdentity(info)

		ft.cursorMu.Lock()
		if ft.cursor.Inode == inode && ft.cursor.Dev == dev && ft.cursor.Offset > 0 {
			if info.Size() < ft.cursor.Offset {
				// File was truncated (copytruncate): reset to beginning.
				ft.cursor.Offset = 0
				ft.cursor.Gen++
			}
			// Resume from cursor (possibly reset to 0 above).
			if _, err := f.Seek(ft.cursor.Offset, io.SeekStart); err != nil {
				ft.cursorMu.Unlock()
				f.Close()
				log.Printf("tailer: seek %s: %v", ft.path, err)
				return nil, fmt.Errorf("seek: %w", err)
			}
		} else {
			if ft.cursor.Inode != 0 && (ft.cursor.Inode != inode || ft.cursor.Dev != dev) {
				ft.cursor.Gen++
			}
			ft.cursor.Inode = inode
			ft.cursor.Dev = dev
			// No checkpoint: read from the beginning so no existing logs are skipped.
			ft.cursor.Offset = 0
		}
		ft.cursorMu.Unlock()

		return f, nil
	}
}

// tailFile reads lines from f using fsnotify, checking for rotation every 5s.
func (ft *FileTailer) tailFile(ctx context.Context, f *os.File) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("tailer: fsnotify: %v", err)
		return
	}
	defer watcher.Close()

	if err := watcher.Add(ft.path); err != nil {
		log.Printf("tailer: watch %s: %v", ft.path, err)
	}

	rotateTicker := time.NewTicker(rotateCheckInterval)
	defer rotateTicker.Stop()

	ft.readLines(f)

	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				ft.readLines(f)
			}

		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}

		case <-rotateTicker.C:
			rotated := ft.checkRotation(f)
			if rotated {
				return
			}
		}
	}
}

// readLines reads all available lines from the current file position and sends them.
func (ft *FileTailer) readLines(f *os.File) {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		if ft.ctx.Err() != nil {
			break
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		cp := make([]byte, len(line))
		copy(cp, line)

		select {
		case ft.outChan <- model.RawLog{Line: cp, Source: "file:" + ft.path}:
		case <-ft.ctx.Done():
			return
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("tailer: scan %s: %v", ft.path, err)
	}

	pos, err := f.Seek(0, io.SeekCurrent)
	if err == nil {
		info, sErr := f.Stat()
		ft.cursorMu.Lock()
		ft.cursor.Offset = pos
		if sErr == nil {
			ft.cursor.FileSize = info.Size()
		}
		ft.cursorMu.Unlock()
	}
}

// checkRotation detects logrotate rename or copytruncate.
// Returns true if the file was rotated (caller should reopen).
func (ft *FileTailer) checkRotation(f *os.File) bool {
	pathInfo, err := os.Stat(ft.path)
	if err != nil {
		// File removed — will be reopened on next iteration.
		return true
	}

	pathInode, pathDev := fileIdentity(pathInfo)

	fdInfo, err := f.Stat()
	if err != nil {
		return true
	}

	fdInode, fdDev := fileIdentity(fdInfo)

	// Rename rotation: path now points to a different inode.
	if pathInode != fdInode || pathDev != fdDev {
		ft.cursorMu.Lock()
		ft.cursor.Inode = pathInode
		ft.cursor.Dev = pathDev
		ft.cursor.Offset = 0
		ft.cursor.Gen++
		ft.cursorMu.Unlock()
		return true
	}

	// Copytruncate: same inode but file got smaller.
	ft.cursorMu.Lock()
	if pathInfo.Size() < ft.cursor.Offset {
		ft.cursor.Offset = 0
		ft.cursor.Gen++
		ft.cursorMu.Unlock()
		return true
	}
	ft.cursorMu.Unlock()

	return false
}

// seekToTail seeks the file to approximately initialTailLines lines from
// the end. It estimates the byte offset, seeks there, then skips forward
// past the first partial line. Returns the final offset. If the file is
// small enough, returns 0 to read from the beginning.
func seekToTail(f *os.File, fileSize int64) int64 {
	estimatedBytes := int64(initialTailLines) * int64(estimatedLineSize)
	if fileSize <= estimatedBytes {
		// File is small enough to read entirely.
		f.Seek(0, io.SeekStart)
		return 0
	}

	seekPos := fileSize - estimatedBytes
	f.Seek(seekPos, io.SeekStart)

	// Skip the first partial line by reading until the next newline.
	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		f.Seek(seekPos, io.SeekStart)
		return seekPos
	}
	for i := 0; i < n; i++ {
		if buf[i] == '\n' {
			offset := seekPos + int64(i+1)
			f.Seek(offset, io.SeekStart)
			return offset
		}
	}
	// No newline found in buffer, just use the seek position.
	f.Seek(seekPos, io.SeekStart)
	return seekPos
}

// fileIdentity extracts inode and device numbers from FileInfo.
func fileIdentity(info os.FileInfo) (inode, dev uint64) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return stat.Ino, uint64(stat.Dev)
}

