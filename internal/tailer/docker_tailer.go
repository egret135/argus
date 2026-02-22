package tailer

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/admin/argus/internal/model"
	"github.com/admin/argus/internal/parser"
	"github.com/admin/argus/internal/storage"
)

const (
	maxBackoff      = 30 * time.Second
	maxLastTSHashes = 16
	dockerSocket    = "/var/run/docker.sock"
)

// dockerHTTPClient returns an *http.Client that dials the Docker Engine
// API over the Unix socket.
func dockerHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", dockerSocket)
			},
		},
	}
}

// DockerTailer follows logs from a Docker container by name and sends
// extracted log lines (after envelope parsing) to outChan.
type DockerTailer struct {
	containerName string
	outChan       chan<- model.RawLog
	cursor        storage.DockerCursor
	cursorMu      sync.Mutex

	dropped atomic.Int64
}

// NewDockerTailer creates a DockerTailer for the given container name.
func NewDockerTailer(containerName string, outChan chan<- model.RawLog, cursor storage.DockerCursor) *DockerTailer {
	return &DockerTailer{
		containerName: containerName,
		outChan:       outChan,
		cursor:        cursor,
	}
}

// Source returns the source identifier for this tailer.
func (dt *DockerTailer) Source() string {
	return "docker:" + dt.containerName
}

// GetCursor returns the current cursor under lock.
func (dt *DockerTailer) GetCursor() storage.DockerCursor {
	dt.cursorMu.Lock()
	c := dt.cursor
	dt.cursorMu.Unlock()
	return c
}

// Run connects to the Docker daemon and tails container logs until ctx is
// cancelled. On errors it reconnects with exponential backoff.
func (dt *DockerTailer) Run(ctx context.Context) {
	backoff := time.Second

	for {
		err := dt.follow(ctx)
		if ctx.Err() != nil {
			return
		}

		log.Printf("tailer: docker %s: stream ended: %v, retrying in %v", dt.containerName, err, backoff)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// follow performs a single follow session: connects, finds the container,
// and reads logs until an error or context cancellation.
func (dt *DockerTailer) follow(ctx context.Context) error {
	cli := dockerHTTPClient()

	containerID, err := dt.findContainer(ctx, cli)
	if err != nil {
		return fmt.Errorf("find container %s: %w", dt.containerName, err)
	}

	since := dt.computeSince()

	q := url.Values{}
	q.Set("follow", "1")
	q.Set("stdout", "1")
	q.Set("stderr", "1")
	q.Set("timestamps", "1")
	if since != "" {
		q.Set("since", since)
	}

	reqURL := fmt.Sprintf("http://localhost/containers/%s/logs?%s", containerID, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("create logs request: %w", err)
	}

	resp, err := cli.Do(req)
	if err != nil {
		return fmt.Errorf("container logs %s: %w", containerID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("container logs %s: status %d: %s", containerID, resp.StatusCode, body)
	}

	return dt.readStream(ctx, resp.Body)
}

// dockerContainerEntry is the minimal structure returned by
// GET /containers/json.
type dockerContainerEntry struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
}

// findContainer looks up the container ID by name using the Docker Engine API.
func (dt *DockerTailer) findContainer(ctx context.Context, cli *http.Client) (string, error) {
	reqURL := "http://localhost/containers/json?all=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("create list request: %w", err)
	}

	resp, err := cli.Do(req)
	if err != nil {
		return "", fmt.Errorf("list containers: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("list containers: status %d: %s", resp.StatusCode, body)
	}

	var containers []dockerContainerEntry
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return "", fmt.Errorf("decode container list: %w", err)
	}

	for _, c := range containers {
		for _, name := range c.Names {
			trimmed := strings.TrimPrefix(name, "/")
			if trimmed == dt.containerName {
				return c.ID, nil
			}
		}
	}

	return "", fmt.Errorf("container %q not found", dt.containerName)
}

// computeSince returns the "since" parameter for the Docker logs API.
// It floors the last timestamp to the second and subtracts 1 second for
// the overlap strategy.
func (dt *DockerTailer) computeSince() string {
	dt.cursorMu.Lock()
	lastTS := dt.cursor.LastTS
	dt.cursorMu.Unlock()

	if lastTS == "" {
		return ""
	}

	t, err := time.Parse(time.RFC3339Nano, lastTS)
	if err != nil {
		return ""
	}

	floored := t.Truncate(time.Second).Add(-time.Second)
	return strconv.FormatInt(floored.Unix(), 10)
}

// readStream demultiplexes the Docker multiplexed log stream and processes
// each line. The Docker stream format uses an 8-byte header per frame:
//
//	[0]   stream type (0=stdin, 1=stdout, 2=stderr)
//	[1-3] padding (zeros)
//	[4-7] payload length (big-endian uint32)
func (dt *DockerTailer) readStream(ctx context.Context, reader io.Reader) error {
	pr, pw := io.Pipe()
	defer pr.Close()

	errCh := make(chan error, 1)
	go func() {
		err := demuxDockerStream(reader, pw)
		pw.CloseWithError(err)
		errCh <- err
	}()

	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		n, err := pr.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			buf = dt.processBuffer(buf)
		}
		if err != nil {
			if err == io.EOF {
				dt.processBuffer(buf)
				return <-errCh
			}
			return err
		}
	}
}

// demuxDockerStream reads the Docker multiplexed stream (8-byte header
// per frame) and writes the raw payload to w.
func demuxDockerStream(r io.Reader, w io.Writer) error {
	header := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return io.EOF
			}
			return err
		}
		size := binary.BigEndian.Uint32(header[4:8])
		if size == 0 {
			continue
		}
		if _, err := io.CopyN(w, r, int64(size)); err != nil {
			return err
		}
	}
}

// processBuffer extracts complete lines from buf, processes each, and
// returns the remaining partial line.
func (dt *DockerTailer) processBuffer(buf []byte) []byte {
	for {
		idx := -1
		for i, b := range buf {
			if b == '\n' {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}

		line := buf[:idx]
		buf = buf[idx+1:]

		if len(line) == 0 {
			continue
		}

		dt.processLine(line)
	}

	// Compact buffer.
	if len(buf) > 0 {
		compact := make([]byte, len(buf))
		copy(compact, buf)
		return compact
	}
	return buf[:0]
}

// processLine handles a single log line: parses envelope, deduplicates,
// and sends to outChan.
func (dt *DockerTailer) processLine(line []byte) {
	// Docker timestamps prefix: "2006-01-02T15:04:05.999999999Z <log>"
	ts, payload := splitDockerTimestamp(line)

	extracted, err := parser.ParseDockerEnvelope(payload)
	if err != nil {
		extracted = payload
	}

	lineHash := crc32Hash(payload)

	dt.cursorMu.Lock()
	defer dt.cursorMu.Unlock()

	if ts != "" && dt.cursor.LastTS != "" {
		if ts < dt.cursor.LastTS {
			return
		}
		if ts == dt.cursor.LastTS {
			for _, h := range dt.cursor.LastTSHashes {
				if h == lineHash {
					return
				}
			}
		}
	}

	cp := make([]byte, len(extracted))
	copy(cp, extracted)

	select {
	case dt.outChan <- model.RawLog{Line: cp, Source: "docker:" + dt.containerName}:
	default:
		dt.dropped.Add(1)
	}

	if ts != dt.cursor.LastTS {
		dt.cursor.LastTS = ts
		dt.cursor.LastTSHashes = []string{lineHash}
	} else {
		dt.cursor.LastTSHashes = append(dt.cursor.LastTSHashes, lineHash)
		if len(dt.cursor.LastTSHashes) > maxLastTSHashes {
			dt.cursor.LastTSHashes = dt.cursor.LastTSHashes[len(dt.cursor.LastTSHashes)-maxLastTSHashes:]
		}
	}
}

// splitDockerTimestamp splits a Docker log line (with timestamps=true)
// into the timestamp and the remaining payload.
func splitDockerTimestamp(line []byte) (string, []byte) {
	for i, b := range line {
		if b == ' ' {
			return string(line[:i]), line[i+1:]
		}
	}
	return "", line
}

// crc32Hash returns a hex-encoded CRC32 IEEE checksum of data.
func crc32Hash(data []byte) string {
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE(data))
}
