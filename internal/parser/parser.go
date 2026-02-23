package parser

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/admin/argus/internal/model"
)

var validLevels = map[string]bool{
	"DEBUG": true,
	"INFO":  true,
	"WARN":  true,
	"ERROR": true,
	"FATAL": true,
}

func Parse(line []byte, source string) (model.LogEntry, error) {
	var entry model.LogEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return model.LogEntry{}, err
	}

	if entry.Timestamp == "" {
		return model.LogEntry{}, errors.New("missing required field: timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err != nil {
		if _, err := time.Parse(time.RFC3339, entry.Timestamp); err != nil {
			return model.LogEntry{}, fmt.Errorf("invalid timestamp format (expected RFC3339): %s", entry.Timestamp)
		}
	}
	if entry.Level == "" {
		return model.LogEntry{}, errors.New("missing required field: level")
	}
	if entry.Service == "" {
		return model.LogEntry{}, errors.New("missing required field: service")
	}
	if entry.Message == "" {
		return model.LogEntry{}, errors.New("missing required field: message")
	}

	entry.Level = strings.ToUpper(entry.Level)
	if !validLevels[entry.Level] {
		return model.LogEntry{}, errors.New("invalid level: " + entry.Level)
	}

	entry.Source = source
	entry.RawJSON = make([]byte, len(line))
	copy(entry.RawJSON, line)

	return entry, nil
}

type dockerEnvelope struct {
	Log string `json:"log"`
}

func ParseDockerEnvelope(line []byte) ([]byte, error) {
	var env dockerEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return nil, err
	}
	if env.Log == "" {
		return line, nil
	}
	return []byte(strings.TrimRight(env.Log, "\n")), nil
}
