package alert

import (
	"testing"
	"time"
)

func TestNewLimiter(t *testing.T) {
	l := NewLimiter(30 * time.Second)
	if l.cooldown != 30*time.Second {
		t.Errorf("cooldown = %v, want %v", l.cooldown, 30*time.Second)
	}
	if l.cooldownMap == nil {
		t.Error("cooldownMap is nil")
	}
	if l.inflight == nil {
		t.Error("inflight is nil")
	}
}

func TestInflightLifecycle(t *testing.T) {
	l := NewLimiter(time.Minute)
	fp := "abc123"

	if l.IsInflight(fp) {
		t.Fatal("expected not inflight initially")
	}

	l.SetInflight(fp)
	if !l.IsInflight(fp) {
		t.Fatal("expected inflight after SetInflight")
	}

	l.ClearInflight(fp)
	if l.IsInflight(fp) {
		t.Fatal("expected not inflight after ClearInflight")
	}
}

func TestCooldown(t *testing.T) {
	l := NewLimiter(10 * time.Millisecond)
	key := "svc:error happened"

	l.SetCooldown(key)
	if !l.IsCoolingDown(key) {
		t.Fatal("expected cooling down immediately after SetCooldown")
	}

	time.Sleep(15 * time.Millisecond)
	if l.IsCoolingDown(key) {
		t.Fatal("expected cooldown to have expired")
	}
}

func TestCleanupInflight(t *testing.T) {
	l := NewLimiter(time.Minute)

	l.SetInflight("old")
	l.inflight["old"] = time.Now().Add(-10 * time.Minute)

	l.SetInflight("recent")

	removed := l.CleanupInflight()
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if l.IsInflight("old") {
		t.Error("expected 'old' to be removed")
	}
	if !l.IsInflight("recent") {
		t.Error("expected 'recent' to still be inflight")
	}
}

func TestNormMessage(t *testing.T) {
	l := NewLimiter(time.Minute)

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "IP masking",
			input: "connection from 192.168.1.100 refused",
			want:  "connection from <ip> refused",
		},
		{
			name:  "hex masking",
			input: "request id 1a2b3c4d5e6f7a8b failed",
			want:  "request id <id> failed",
		},
		{
			name:  "numeric masking",
			input: "timeout after 3000 ms on port 8080",
			want:  "timeout after <N> ms on port <N>",
		},
		{
			name:  "whitespace normalization",
			input: "too   many   spaces",
			want:  "too many spaces",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := l.NormMessage(tc.input)
			if got != tc.want {
				t.Errorf("NormMessage(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestCooldownKey(t *testing.T) {
	l := NewLimiter(time.Minute)
	key := l.CooldownKey("billing", "timeout after 3000 ms")
	want := "billing:timeout after <N> ms"
	if key != want {
		t.Errorf("CooldownKey = %q, want %q", key, want)
	}
}
