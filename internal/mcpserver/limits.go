package mcpserver

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/darioajr/skupper-scope/internal/config"
)

var errLimited = errors.New("MCP invocation limit exceeded")

type limitEntry struct {
	start        time.Time
	last         time.Time
	blockedUntil time.Time
	count        int
	violations   int
}

// invocationLimiter is shared by all sessions of one server. Anonymous callers
// share one user bucket, so reconnecting cannot reset their aggregate limit.
// State is bounded and expires on demand; no background goroutine is needed.
type invocationLimiter struct {
	mu      sync.Mutex
	cfg     config.Limits
	entries map[string]*limitEntry
}

func newInvocationLimiter(cfg config.Limits) (*invocationLimiter, error) {
	if cfg.PerUser <= 0 || cfg.PerSession <= 0 || cfg.Window <= 0 || cfg.Cooldown <= 0 ||
		cfg.BreakerThreshold <= 0 || cfg.MaxEntries < 2 || cfg.MaxEntries > 100000 || cfg.AlertOwner == "" {
		return nil, errors.New("mcpserver: positive invocation limits and an alert owner are required")
	}
	return &invocationLimiter{cfg: cfg, entries: make(map[string]*limitEntry)}, nil
}

func (l *invocationLimiter) allow(now time.Time, user, session string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Expired entries are removed only under pressure, keeping normal calls O(1).
	if len(l.entries)+2 > l.cfg.MaxEntries {
		for key, entry := range l.entries {
			if now.Sub(entry.last) >= l.cfg.Window && !now.Before(entry.blockedUntil) {
				delete(l.entries, key)
			}
		}
	}
	keys := []string{"user:" + user, "session:" + user + ":" + session}
	limits := []int{l.cfg.PerUser, l.cfg.PerSession}
	missing := 0
	for _, key := range keys {
		if l.entries[key] == nil {
			missing++
		}
	}
	if len(l.entries)+missing > l.cfg.MaxEntries {
		return false, fmt.Errorf("%w: limiter capacity reached; retry later", errLimited)
	}
	opened := false
	denied := false
	for i, key := range keys {
		e := l.entries[key]
		if e == nil {
			e = &limitEntry{start: now}
			l.entries[key] = e
		}
		e.last = now
		if now.Before(e.blockedUntil) {
			denied = true
			continue
		}
		if now.Sub(e.start) >= l.cfg.Window {
			e.start, e.count, e.violations = now, 0, 0
		}
		e.count++
		if e.count > limits[i] {
			denied = true
			e.violations++
			if e.violations >= l.cfg.BreakerThreshold {
				e.blockedUntil = now.Add(l.cfg.Cooldown)
				e.start = e.blockedUntil
				e.count, e.violations = 0, 0
				opened = true
			}
		}
	}
	if denied {
		return opened, fmt.Errorf("%w: user/session budget or circuit breaker; retry after the configured window/cooldown", errLimited)
	}
	return false, nil
}
