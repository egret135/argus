package parser

import (
	"encoding/json"
	"testing"
)

func validJSON() []byte {
	return []byte(`{"timestamp":"2025-01-01T00:00:00Z","level":"ERROR","service":"api","message":"something broke","trace_id":"abc123"}`)
}

func TestParse_ValidJSON(t *testing.T) {
	entry, err := Parse(validJSON(), "docker:api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Timestamp != "2025-01-01T00:00:00Z" {
		t.Errorf("Timestamp = %q, want %q", entry.Timestamp, "2025-01-01T00:00:00Z")
	}
	if entry.Level != "ERROR" {
		t.Errorf("Level = %q, want %q", entry.Level, "ERROR")
	}
	if entry.Service != "api" {
		t.Errorf("Service = %q, want %q", entry.Service, "api")
	}
	if entry.Message != "something broke" {
		t.Errorf("Message = %q, want %q", entry.Message, "something broke")
	}
	if entry.TraceID != "abc123" {
		t.Errorf("TraceID = %q, want %q", entry.TraceID, "abc123")
	}
}

func TestParse_MissingTimestamp(t *testing.T) {
	line := []byte(`{"level":"ERROR","service":"api","message":"boom"}`)
	_, err := Parse(line, "src")
	if err == nil {
		t.Fatal("expected error for missing timestamp")
	}
}

func TestParse_MissingLevel(t *testing.T) {
	line := []byte(`{"timestamp":"2025-01-01T00:00:00Z","service":"api","message":"boom"}`)
	_, err := Parse(line, "src")
	if err == nil {
		t.Fatal("expected error for missing level")
	}
}

func TestParse_MissingService(t *testing.T) {
	line := []byte(`{"timestamp":"2025-01-01T00:00:00Z","level":"ERROR","message":"boom"}`)
	_, err := Parse(line, "src")
	if err == nil {
		t.Fatal("expected error for missing service")
	}
}

func TestParse_MissingMessage(t *testing.T) {
	line := []byte(`{"timestamp":"2025-01-01T00:00:00Z","level":"ERROR","service":"api"}`)
	_, err := Parse(line, "src")
	if err == nil {
		t.Fatal("expected error for missing message")
	}
}

func TestParse_InvalidLevel(t *testing.T) {
	line := []byte(`{"timestamp":"2025-01-01T00:00:00Z","level":"TRACE","service":"api","message":"boom"}`)
	_, err := Parse(line, "src")
	if err == nil {
		t.Fatal("expected error for invalid level")
	}
}

func TestParse_LevelCaseInsensitive(t *testing.T) {
	line := []byte(`{"timestamp":"2025-01-01T00:00:00Z","level":"error","service":"api","message":"boom"}`)
	entry, err := Parse(line, "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Level != "ERROR" {
		t.Errorf("Level = %q, want %q", entry.Level, "ERROR")
	}
}

func TestParse_SetsSourceAndRawJSON(t *testing.T) {
	line := validJSON()
	entry, err := Parse(line, "docker:web")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Source != "docker:web" {
		t.Errorf("Source = %q, want %q", entry.Source, "docker:web")
	}
	if !json.Valid(entry.RawJSON) {
		t.Error("RawJSON is not valid JSON")
	}
	if string(entry.RawJSON) != string(line) {
		t.Errorf("RawJSON = %q, want %q", entry.RawJSON, line)
	}
}

func TestParseDockerEnvelope_Valid(t *testing.T) {
	inner := `{"timestamp":"2025-01-01T00:00:00Z","level":"ERROR","service":"api","message":"boom"}`
	envelope, _ := json.Marshal(dockerEnvelope{Log: inner + "\n"})
	got, err := ParseDockerEnvelope(envelope)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != inner {
		t.Errorf("got %q, want %q", got, inner)
	}
}

func TestParseDockerEnvelope_NonEnvelope(t *testing.T) {
	line := []byte(`{"timestamp":"2025-01-01T00:00:00Z","level":"ERROR","service":"api","message":"boom"}`)
	got, err := ParseDockerEnvelope(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(line) {
		t.Errorf("got %q, want %q", got, line)
	}
}
