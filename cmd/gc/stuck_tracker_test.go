package main

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

func sessionBeadWithWoke(name, lastWoke string) beads.Bead {
	return beads.Bead{
		ID: "test-bead",
		Metadata: map[string]string{
			"session_name": name,
			"last_woke_at": lastWoke,
		},
	}
}

func TestMemoryStuckTracker_NotStuckWhenNoTimeout(t *testing.T) {
	st := newStuckTracker()
	now := time.Now()
	woke := now.Add(-2 * time.Hour).Format(time.RFC3339)
	session := sessionBeadWithWoke("worker", woke)
	// No timeout set — should never be stuck
	if st.checkStuck(session, now) {
		t.Error("checkStuck = true, want false when no timeout configured")
	}
}

func TestMemoryStuckTracker_NotStuckWhenUnderThreshold(t *testing.T) {
	st := newStuckTracker()
	st.setTimeout("worker", 30*time.Minute)
	now := time.Now()
	woke := now.Add(-10 * time.Minute).Format(time.RFC3339)
	session := sessionBeadWithWoke("worker", woke)
	if st.checkStuck(session, now) {
		t.Error("checkStuck = true, want false when under threshold")
	}
}

func TestMemoryStuckTracker_StuckWhenOverThreshold(t *testing.T) {
	st := newStuckTracker()
	st.setTimeout("worker", 30*time.Minute)
	now := time.Now()
	woke := now.Add(-45 * time.Minute).Format(time.RFC3339)
	session := sessionBeadWithWoke("worker", woke)
	if !st.checkStuck(session, now) {
		t.Error("checkStuck = false, want true when over threshold")
	}
}

func TestMemoryStuckTracker_NotStuckWhenNoLastWoke(t *testing.T) {
	st := newStuckTracker()
	st.setTimeout("worker", 5*time.Minute)
	now := time.Now()
	session := beads.Bead{
		ID:       "test-bead",
		Metadata: map[string]string{"session_name": "worker"},
	}
	if st.checkStuck(session, now) {
		t.Error("checkStuck = true, want false when last_woke_at is empty")
	}
}

func TestMemoryStuckTracker_SetTimeoutZeroDisables(t *testing.T) {
	st := newStuckTracker()
	st.setTimeout("worker", 5*time.Minute)
	st.setTimeout("worker", 0) // disable
	now := time.Now()
	woke := now.Add(-2 * time.Hour).Format(time.RFC3339)
	session := sessionBeadWithWoke("worker", woke)
	if st.checkStuck(session, now) {
		t.Error("checkStuck = true, want false after timeout disabled with 0")
	}
}

func TestMemoryStuckTracker_JustUnderThresholdNotStuck(t *testing.T) {
	st := newStuckTracker()
	timeout := 30 * time.Minute
	st.setTimeout("worker", timeout)
	now := time.Now()
	// 1 second under the threshold — not stuck
	woke := now.Add(-(timeout - time.Second)).Format(time.RFC3339)
	session := sessionBeadWithWoke("worker", woke)
	if st.checkStuck(session, now) {
		t.Error("checkStuck = true, want false just under threshold")
	}
}

func TestMemoryStuckTracker_DifferentSessions(t *testing.T) {
	st := newStuckTracker()
	st.setTimeout("slow-worker", 2*time.Hour)
	st.setTimeout("fast-worker", 5*time.Minute)
	now := time.Now()
	woke := now.Add(-30 * time.Minute).Format(time.RFC3339)

	slowSession := sessionBeadWithWoke("slow-worker", woke)
	fastSession := sessionBeadWithWoke("fast-worker", woke)

	if st.checkStuck(slowSession, now) {
		t.Error("slow-worker: checkStuck = true, want false (2h timeout, 30m elapsed)")
	}
	if !st.checkStuck(fastSession, now) {
		t.Error("fast-worker: checkStuck = false, want true (5m timeout, 30m elapsed)")
	}
}
