package chatsession

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

func TestCreate(t *testing.T) {
	store := beads.NewMemStore()
	sp := session.NewFake()
	mgr := NewManager(store, sp)

	info, err := mgr.Create(context.Background(), "helper", "my chat", "claude", "/tmp", "claude", nil, session.Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Template != "helper" {
		t.Errorf("Template = %q, want %q", info.Template, "helper")
	}
	if info.Title != "my chat" {
		t.Errorf("Title = %q, want %q", info.Title, "my chat")
	}
	if info.State != StateActive {
		t.Errorf("State = %q, want %q", info.State, StateActive)
	}
	if info.ID == "" {
		t.Error("ID is empty")
	}

	// Verify the tmux session was started.
	if !sp.IsRunning(info.SessionName) {
		t.Error("runtime session not started")
	}

	// Verify bead was created with correct type and labels.
	b, err := store.Get(info.ID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if b.Type != BeadType {
		t.Errorf("bead Type = %q, want %q", b.Type, BeadType)
	}
	if b.Status != "open" {
		t.Errorf("bead Status = %q, want %q", b.Status, "open")
	}
	hasLabel := false
	for _, l := range b.Labels {
		if l == LabelSession {
			hasLabel = true
		}
	}
	if !hasLabel {
		t.Errorf("bead missing label %q", LabelSession)
	}
}

func TestSuspendAndResume(t *testing.T) {
	store := beads.NewMemStore()
	sp := session.NewFake()
	mgr := NewManager(store, sp)

	info, err := mgr.Create(context.Background(), "helper", "", "claude", "/tmp", "claude", nil, session.Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Suspend.
	if err := mgr.Suspend(info.ID); err != nil {
		t.Fatalf("Suspend: %v", err)
	}

	// Verify runtime session stopped.
	if sp.IsRunning(info.SessionName) {
		t.Error("runtime session should be stopped after suspend")
	}

	// Verify bead state updated.
	got, err := mgr.Get(info.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateSuspended {
		t.Errorf("State = %q, want %q", got.State, StateSuspended)
	}

	// Suspend again is idempotent.
	if err := mgr.Suspend(info.ID); err != nil {
		t.Fatalf("Suspend (idempotent): %v", err)
	}

	// Resume via Attach.
	err = mgr.Attach(context.Background(), info.ID, "claude --resume", session.Config{})
	if err != nil {
		t.Fatalf("Attach (resume): %v", err)
	}

	// Verify runtime session restarted.
	if !sp.IsRunning(info.SessionName) {
		t.Error("runtime session should be running after resume")
	}

	// Verify state back to active.
	got, err = mgr.Get(info.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateActive {
		t.Errorf("State = %q, want %q", got.State, StateActive)
	}
}

func TestClose(t *testing.T) {
	store := beads.NewMemStore()
	sp := session.NewFake()
	mgr := NewManager(store, sp)

	info, err := mgr.Create(context.Background(), "helper", "", "claude", "/tmp", "claude", nil, session.Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Close active session.
	if err := mgr.Close(info.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify runtime stopped.
	if sp.IsRunning(info.SessionName) {
		t.Error("runtime session should be stopped after close")
	}

	// Verify bead closed.
	b, err := store.Get(info.ID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if b.Status != "closed" {
		t.Errorf("bead Status = %q, want %q", b.Status, "closed")
	}

	// Close again is idempotent.
	if err := mgr.Close(info.ID); err != nil {
		t.Fatalf("Close (idempotent): %v", err)
	}
}

func TestCloseSuspended(t *testing.T) {
	store := beads.NewMemStore()
	sp := session.NewFake()
	mgr := NewManager(store, sp)

	info, err := mgr.Create(context.Background(), "helper", "", "claude", "/tmp", "claude", nil, session.Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.Suspend(info.ID); err != nil {
		t.Fatalf("Suspend: %v", err)
	}

	// Close suspended session.
	if err := mgr.Close(info.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	b, err := store.Get(info.ID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if b.Status != "closed" {
		t.Errorf("bead Status = %q, want %q", b.Status, "closed")
	}
}

func TestList(t *testing.T) {
	store := beads.NewMemStore()
	sp := session.NewFake()
	mgr := NewManager(store, sp)

	// Create two sessions with different templates.
	_, err := mgr.Create(context.Background(), "helper", "first", "claude", "/tmp", "claude", nil, session.Config{})
	if err != nil {
		t.Fatalf("Create 1: %v", err)
	}
	info2, err := mgr.Create(context.Background(), "review", "second", "claude", "/tmp", "claude", nil, session.Config{})
	if err != nil {
		t.Fatalf("Create 2: %v", err)
	}

	// Suspend the second one.
	if err := mgr.Suspend(info2.ID); err != nil {
		t.Fatalf("Suspend: %v", err)
	}

	// List all (default excludes closed).
	sessions, err := mgr.List("", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("List returned %d sessions, want 2", len(sessions))
	}

	// Filter by state.
	active, err := mgr.List("active", "")
	if err != nil {
		t.Fatalf("List active: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("List active returned %d, want 1", len(active))
	}

	suspended, err := mgr.List("suspended", "")
	if err != nil {
		t.Fatalf("List suspended: %v", err)
	}
	if len(suspended) != 1 {
		t.Errorf("List suspended returned %d, want 1", len(suspended))
	}

	// Filter by template.
	helpers, err := mgr.List("", "helper")
	if err != nil {
		t.Fatalf("List template: %v", err)
	}
	if len(helpers) != 1 {
		t.Errorf("List template=helper returned %d, want 1", len(helpers))
	}
}

func TestPeek(t *testing.T) {
	store := beads.NewMemStore()
	sp := session.NewFake()
	mgr := NewManager(store, sp)

	info, err := mgr.Create(context.Background(), "helper", "", "claude", "/tmp", "claude", nil, session.Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Set canned peek output on the session name.
	sp.SetPeekOutput(info.SessionName, "hello world")

	out, err := mgr.Peek(info.ID, 50)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if out != "hello world" {
		t.Errorf("Peek output = %q, want %q", out, "hello world")
	}
}

func TestPeekSuspended(t *testing.T) {
	store := beads.NewMemStore()
	sp := session.NewFake()
	mgr := NewManager(store, sp)

	info, err := mgr.Create(context.Background(), "helper", "", "claude", "/tmp", "claude", nil, session.Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.Suspend(info.ID); err != nil {
		t.Fatalf("Suspend: %v", err)
	}

	_, err = mgr.Peek(info.ID, 50)
	if err == nil {
		t.Error("Peek on suspended session should error")
	}
}

func TestAttachClosedErrors(t *testing.T) {
	store := beads.NewMemStore()
	sp := session.NewFake()
	mgr := NewManager(store, sp)

	info, err := mgr.Create(context.Background(), "helper", "", "claude", "/tmp", "claude", nil, session.Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.Close(info.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err = mgr.Attach(context.Background(), info.ID, "claude --resume", session.Config{})
	if err == nil {
		t.Error("Attach to closed session should error")
	}
}

func TestSessionNameFor(t *testing.T) {
	tests := []struct {
		beadID string
		want   string
	}{
		{"gc-1", "s-gc-1"},
		{"gc-42", "s-gc-42"},
	}
	for _, tt := range tests {
		got := sessionNameFor(tt.beadID)
		if got != tt.want {
			t.Errorf("sessionNameFor(%q) = %q, want %q", tt.beadID, got, tt.want)
		}
	}
}

func TestListExcludesClosedFromActiveFilter(t *testing.T) {
	store := beads.NewMemStore()
	sp := session.NewFake()
	mgr := NewManager(store, sp)

	info, err := mgr.Create(context.Background(), "helper", "", "claude", "/tmp", "claude", nil, session.Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.Close(info.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Filtering by "active" should NOT return the closed session.
	active, err := mgr.List("active", "")
	if err != nil {
		t.Fatalf("List active: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("List active returned %d, want 0 (closed session leaked)", len(active))
	}
}

func TestAttachActiveReattach(t *testing.T) {
	store := beads.NewMemStore()
	sp := session.NewFake()
	mgr := NewManager(store, sp)

	info, err := mgr.Create(context.Background(), "helper", "", "claude", "/tmp", "claude", nil, session.Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Attach to an active session — should reattach without restarting.
	err = mgr.Attach(context.Background(), info.ID, "claude --resume", session.Config{})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Verify state is still active.
	got, err := mgr.Get(info.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateActive {
		t.Errorf("State = %q, want %q", got.State, StateActive)
	}
}

func TestSuspendCrashedSession(t *testing.T) {
	store := beads.NewMemStore()
	sp := session.NewFake()
	mgr := NewManager(store, sp)

	info, err := mgr.Create(context.Background(), "helper", "", "claude", "/tmp", "claude", nil, session.Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Simulate crash by stopping the runtime behind the manager's back.
	_ = sp.Stop(info.SessionName)

	// Suspend should succeed even though runtime is dead.
	if err := mgr.Suspend(info.ID); err != nil {
		t.Fatalf("Suspend crashed session: %v", err)
	}

	got, err := mgr.Get(info.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != StateSuspended {
		t.Errorf("State = %q, want %q", got.State, StateSuspended)
	}
}

func TestCreateStoresCommand(t *testing.T) {
	store := beads.NewMemStore()
	sp := session.NewFake()
	mgr := NewManager(store, sp)

	info, err := mgr.Create(context.Background(), "helper", "", "claude --dangerously-skip-permissions", "/tmp", "claude", nil, session.Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify the command is stored in the bead metadata.
	b, err := store.Get(info.ID)
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if b.Metadata["command"] != "claude --dangerously-skip-permissions" {
		t.Errorf("stored command = %q, want %q", b.Metadata["command"], "claude --dangerously-skip-permissions")
	}

	// Verify it's accessible via Info.
	if info.Command != "claude --dangerously-skip-permissions" {
		t.Errorf("Info.Command = %q, want %q", info.Command, "claude --dangerously-skip-permissions")
	}
}

func TestCreateFailsCleanup(t *testing.T) {
	store := beads.NewMemStore()
	sp := session.NewFailFake() // all operations fail
	mgr := NewManager(store, sp)

	_, err := mgr.Create(context.Background(), "helper", "", "claude", "/tmp", "claude", nil, session.Config{})
	if err == nil {
		t.Fatal("Create should fail when provider fails")
	}

	// The bead should be closed (cleaned up).
	all, _ := store.List()
	for _, b := range all {
		if b.Type == BeadType && b.Status == "open" {
			t.Errorf("orphan session bead %s left open after failed create", b.ID)
		}
	}
}
