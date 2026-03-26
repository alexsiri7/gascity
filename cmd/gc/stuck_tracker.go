package main

import (
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// stuckTracker detects agents that have been running longer than their
// configured stuck timeout. An agent is "stuck" when it has been alive
// since last_woke_at for longer than the threshold — indicating an infinite
// thinking loop or similar runaway condition. Unlike idleTracker, stuck
// detection triggers when the agent IS active (has been running continuously),
// not when it has gone silent.
//
// Nil means stuck checking is disabled. Follows the same nil-guard pattern
// as idleTracker and crashTracker.
type stuckTracker interface {
	// checkStuck returns true if the agent has been running longer than its
	// configured stuck timeout since its last_woke_at.
	checkStuck(session beads.Bead, now time.Time) bool

	// setTimeout configures the stuck timeout for a session name.
	// Called during agent list construction. Duration of 0 disables.
	setTimeout(sessionName string, timeout time.Duration)
}

// memoryStuckTracker is the production implementation of stuckTracker.
type memoryStuckTracker struct {
	mu       sync.Mutex
	timeouts map[string]time.Duration // session → stuck timeout
}

// newStuckTracker creates a stuck tracker.
func newStuckTracker() *memoryStuckTracker {
	return &memoryStuckTracker{
		timeouts: make(map[string]time.Duration),
	}
}

func (m *memoryStuckTracker) setTimeout(sessionName string, timeout time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if timeout <= 0 {
		delete(m.timeouts, sessionName)
		return
	}
	m.timeouts[sessionName] = timeout
}

func (m *memoryStuckTracker) checkStuck(session beads.Bead, now time.Time) bool {
	name := session.Metadata["session_name"]
	m.mu.Lock()
	timeout, ok := m.timeouts[name]
	m.mu.Unlock()
	if !ok || timeout <= 0 {
		return false
	}
	lastWoke := session.Metadata["last_woke_at"]
	if lastWoke == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, lastWoke)
	if err != nil {
		return false
	}
	return now.Sub(t) > timeout
}
