package model

// RawLog pairs a raw log line with its source identifier.
type RawLog struct {
	Line   []byte
	Source string
}

// LogEntry is the full log entry parsed from a JSON log line.
type LogEntry struct {
	Timestamp  string         `json:"timestamp"`
	Level      string         `json:"level"`
	Service    string         `json:"service"`
	Message    string         `json:"message"`
	TraceID    string         `json:"trace_id"`
	Caller     string         `json:"caller"`
	StackTrace string         `json:"stack_trace"`
	Extra      map[string]any `json:"extra"`

	Source  string `json:"-"`
	SeqID   uint64 `json:"-"`
	RawJSON []byte `json:"-"`
}

// EntryLite is a lightweight view of a log entry used for index snapshots.
type EntryLite struct {
	SeqID     uint64
	Timestamp string
	Level     string
	Source    string
	Message   string
}

// AlertRequest is sent from the pipeline to an alert worker.
type AlertRequest struct {
	Fingerprint string
	LogEntry    LogEntry
	CooldownKey string
}

// AlertResult is sent back from an alert worker to the pipeline.
type AlertResult struct {
	Fingerprint string
	CooldownKey string
	Success     bool
}
