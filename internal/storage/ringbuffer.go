package storage

import (
	"sync"

	"github.com/admin/argus/internal/model"
)

type RingBuffer struct {
	mu       sync.RWMutex
	slots    []model.LogEntry
	capacity int
	head     uint64
	tail     uint64
}

func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 50000
	}
	return &RingBuffer{
		slots:    make([]model.LogEntry, capacity),
		capacity: capacity,
	}
}

func (rb *RingBuffer) Write(entry model.LogEntry) uint64 {
	rb.mu.Lock()
	seq := rb.tail
	entry.SeqID = seq
	rb.slots[seq%uint64(rb.capacity)] = entry
	rb.tail++
	if rb.tail-rb.head > uint64(rb.capacity) {
		rb.head = rb.tail - uint64(rb.capacity)
	}
	rb.mu.Unlock()
	return seq
}

func (rb *RingBuffer) Read(seqID uint64) (model.LogEntry, bool) {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if seqID < rb.head || seqID >= rb.tail {
		return model.LogEntry{}, false
	}
	entry := rb.slots[seqID%uint64(rb.capacity)]
	return entry, true
}

func (rb *RingBuffer) Range(from, to uint64) []model.LogEntry {
	rb.mu.RLock()
	defer rb.mu.RUnlock()

	if from < rb.head {
		from = rb.head
	}
	if to > rb.tail {
		to = rb.tail
	}
	if from >= to {
		return nil
	}

	result := make([]model.LogEntry, 0, to-from)
	for seq := from; seq < to; seq++ {
		result = append(result, rb.slots[seq%uint64(rb.capacity)])
	}
	return result
}

func (rb *RingBuffer) Head() uint64 {
	rb.mu.RLock()
	h := rb.head
	rb.mu.RUnlock()
	return h
}

func (rb *RingBuffer) Tail() uint64 {
	rb.mu.RLock()
	t := rb.tail
	rb.mu.RUnlock()
	return t
}

func (rb *RingBuffer) Len() int {
	rb.mu.RLock()
	n := int(rb.tail - rb.head)
	rb.mu.RUnlock()
	return n
}

func (rb *RingBuffer) Capacity() int {
	return rb.capacity
}

func (rb *RingBuffer) SnapshotLite() ([]model.EntryLite, uint64) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	n := rb.tail - rb.head
	if n == 0 {
		return nil, rb.tail
	}

	snap := make([]model.EntryLite, 0, n)
	for seq := rb.head; seq < rb.tail; seq++ {
		e := rb.slots[seq%uint64(rb.capacity)]
		snap = append(snap, model.EntryLite{
			SeqID:     e.SeqID,
			Timestamp: e.Timestamp,
			Level:     e.Level,
			Source:    e.Source,
			Message:   e.Message,
		})
	}
	return snap, rb.tail
}

func (rb *RingBuffer) RangeRaw(from, to uint64, batchSize int, fn func([]model.LogEntry) bool) {
	if batchSize <= 0 {
		batchSize = 1024
	}

	rb.mu.RLock()
	if from < rb.head {
		from = rb.head
	}
	if to > rb.tail {
		to = rb.tail
	}
	rb.mu.RUnlock()

	for pos := from; pos < to; {
		end := pos + uint64(batchSize)
		if end > to {
			end = to
		}

		rb.mu.RLock()
		if pos < rb.head {
			pos = rb.head
		}
		if pos >= rb.tail || pos >= end {
			rb.mu.RUnlock()
			return
		}
		if end > rb.tail {
			end = rb.tail
		}

		batch := make([]model.LogEntry, 0, end-pos)
		for seq := pos; seq < end; seq++ {
			batch = append(batch, rb.slots[seq%uint64(rb.capacity)])
		}
		rb.mu.RUnlock()

		if !fn(batch) {
			return
		}
		pos = end
	}
}
