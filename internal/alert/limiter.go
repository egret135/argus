package alert

import (
	"regexp"
	"time"
)

var (
	reIPv4       = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	reHex        = regexp.MustCompile(`\b[0-9a-fA-F]{16,}\b`)
	reNumeric    = regexp.MustCompile(`\b\d{2,}\b`)
	reWhitespace = regexp.MustCompile(`\s+`)
)

// Limiter tracks in-flight alerts and cooldown periods.
// All methods are called from a single Pipeline goroutine, so no locks are needed.
type Limiter struct {
	cooldown    time.Duration
	cooldownMap map[string]time.Time
	inflight    map[string]time.Time
	inflightTTL time.Duration
}

// NewLimiter creates a new Limiter with the given cooldown duration.
func NewLimiter(cooldown time.Duration) *Limiter {
	return &Limiter{
		cooldown:    cooldown,
		cooldownMap: make(map[string]time.Time),
		inflight:    make(map[string]time.Time),
		inflightTTL: 5 * time.Minute,
	}
}

// IsInflight returns true if the given fingerprint is currently in-flight.
func (l *Limiter) IsInflight(fp string) bool {
	_, ok := l.inflight[fp]
	return ok
}

// SetInflight marks the given fingerprint as in-flight.
func (l *Limiter) SetInflight(fp string) {
	l.inflight[fp] = time.Now()
}

// ClearInflight removes the given fingerprint from the in-flight set.
func (l *Limiter) ClearInflight(fp string) {
	delete(l.inflight, fp)
}

// IsCoolingDown returns true if the given key is still within its cooldown period.
func (l *Limiter) IsCoolingDown(key string) bool {
	expiry, ok := l.cooldownMap[key]
	if !ok {
		return false
	}
	return time.Now().Before(expiry)
}

// SetCooldown sets the cooldown expiry for the given key.
func (l *Limiter) SetCooldown(key string) {
	l.cooldownMap[key] = time.Now().Add(l.cooldown)
}

// CleanupInflight removes in-flight entries older than inflightTTL and returns the count removed.
func (l *Limiter) CleanupInflight() int {
	cutoff := time.Now().Add(-l.inflightTTL)
	removed := 0
	for fp, startedAt := range l.inflight {
		if startedAt.Before(cutoff) {
			delete(l.inflight, fp)
			removed++
		}
	}
	return removed
}

// NormMessage normalizes a message for use in cooldown keys.
func (l *Limiter) NormMessage(message string) string {
	s := reIPv4.ReplaceAllString(message, "<ip>")
	s = reHex.ReplaceAllString(s, "<id>")
	s = reNumeric.ReplaceAllString(s, "<N>")
	s = reWhitespace.ReplaceAllString(s, " ")
	if len(s) > 256 {
		s = s[:256]
	}
	return s
}

// CooldownKey returns the cooldown key for a given service and message.
func (l *Limiter) CooldownKey(service, message string) string {
	return service + ":" + l.NormMessage(message)
}
