// Package chatsession manages persistent, resumable chat sessions.
//
// A chat session is a conversation between a human and an agent template
// that can be started, suspended (freeing runtime resources), and resumed
// later. Sessions are backed by beads (type "session") for persistence
// and use session.Provider for runtime management.
package chatsession

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

// State represents the runtime state of a chat session.
type State string

const (
	// StateActive means the conversation has a live runtime session.
	StateActive State = "active"
	// StateSuspended means the conversation is paused with no runtime resources.
	StateSuspended State = "suspended"
)

// BeadType is the bead type for chat sessions.
const BeadType = "session"

// LabelSession is the label applied to all session beads for filtering.
const LabelSession = "gc:session"

// Info holds the user-facing details of a chat session.
type Info struct {
	ID          string
	Template    string
	State       State
	Title       string
	Provider    string
	Command     string // resolved command stored at creation
	WorkDir     string
	SessionName string // tmux session name
	CreatedAt   time.Time
	LastActive  time.Time
	Attached    bool
}

// Manager orchestrates chat session lifecycle using beads for persistence
// and session.Provider for runtime.
type Manager struct {
	store beads.Store
	sp    session.Provider
}

// NewManager creates a Manager backed by the given bead store and session provider.
func NewManager(store beads.Store, sp session.Provider) *Manager {
	return &Manager{store: store, sp: sp}
}

// Create creates a new chat session bead and starts the runtime session.
// The command is the full provider command to execute (e.g., "claude --dangerously-skip-permissions").
// The caller is responsible for attaching after Create returns.
func (m *Manager) Create(ctx context.Context, template, title, command, workDir, provider string, env map[string]string, hints session.Config) (Info, error) {
	// Create the bead first to get the ID.
	b, err := m.store.Create(beads.Bead{
		Title: title,
		Type:  BeadType,
		Labels: []string{
			LabelSession,
			"template:" + template,
		},
		Metadata: map[string]string{
			"template": template,
			"state":    string(StateActive),
			"provider": provider,
			"work_dir": workDir,
			"command":  command,
		},
	})
	if err != nil {
		return Info{}, fmt.Errorf("creating session bead: %w", err)
	}

	// Derive the tmux session name from the bead ID.
	sessName := sessionNameFor(b.ID)

	// Store the session name in metadata.
	if err := m.store.SetMetadata(b.ID, "session_name", sessName); err != nil {
		_ = m.store.Close(b.ID)
		return Info{}, fmt.Errorf("storing session name: %w", err)
	}

	// Build the session config from the hints, overriding command/workdir/env.
	cfg := hints
	cfg.Command = command
	cfg.WorkDir = workDir
	cfg.Env = mergeEnv(cfg.Env, env)

	// Start the runtime session.
	if err := m.sp.Start(ctx, sessName, cfg); err != nil {
		// Clean up the bead on start failure.
		_ = m.store.Close(b.ID)
		return Info{}, fmt.Errorf("starting session: %w", err)
	}

	return m.infoFromBead(b), nil
}

// Attach attaches the user's terminal to the session. If the session is
// suspended, it is resumed first using resumeCommand. If the tmux session
// died (active bead but no process), it is restarted.
func (m *Manager) Attach(ctx context.Context, id string, resumeCommand string, hints session.Config) error {
	b, err := m.store.Get(id)
	if err != nil {
		return fmt.Errorf("getting session: %w", err)
	}
	if b.Type != BeadType {
		return fmt.Errorf("bead %s is not a session (type=%q)", id, b.Type)
	}
	if b.Status == "closed" {
		return fmt.Errorf("session %s is closed", id)
	}

	sessName := b.Metadata["session_name"]
	if sessName == "" {
		sessName = sessionNameFor(id)
	}

	state := State(b.Metadata["state"])

	// If suspended or tmux session is dead, (re)start.
	if state == StateSuspended || !m.sp.IsRunning(sessName) {
		cmd := resumeCommand
		if cmd == "" {
			// Fallback: use no resume (fresh start). Caller should provide
			// the resume command for conversation continuity.
			return fmt.Errorf("session %s is suspended and no resume command provided", id)
		}

		cfg := hints
		cfg.Command = cmd
		cfg.WorkDir = b.Metadata["work_dir"]

		if err := m.sp.Start(ctx, sessName, cfg); err != nil {
			return fmt.Errorf("resuming session: %w", err)
		}
		if err := m.store.SetMetadata(id, "state", string(StateActive)); err != nil {
			_ = m.sp.Stop(sessName) // clean up orphan runtime
			return fmt.Errorf("updating session state: %w", err)
		}
	}

	return m.sp.Attach(sessName)
}

// Suspend saves session state and kills the runtime session.
func (m *Manager) Suspend(id string) error {
	b, err := m.store.Get(id)
	if err != nil {
		return fmt.Errorf("getting session: %w", err)
	}
	if b.Type != BeadType {
		return fmt.Errorf("bead %s is not a session (type=%q)", id, b.Type)
	}
	if b.Status == "closed" {
		return fmt.Errorf("session %s is closed", id)
	}
	if State(b.Metadata["state"]) == StateSuspended {
		return nil // already suspended
	}

	sessName := b.Metadata["session_name"]
	if sessName == "" {
		sessName = sessionNameFor(id)
	}

	// Kill the runtime session (skip if already dead).
	if m.sp.IsRunning(sessName) {
		if err := m.sp.Stop(sessName); err != nil {
			return fmt.Errorf("stopping runtime session: %w", err)
		}
	}

	// Update state.
	if err := m.store.SetMetadata(id, "state", string(StateSuspended)); err != nil {
		return fmt.Errorf("updating session state: %w", err)
	}

	return nil
}

// Close ends a conversation permanently.
func (m *Manager) Close(id string) error {
	b, err := m.store.Get(id)
	if err != nil {
		return fmt.Errorf("getting session: %w", err)
	}
	if b.Type != BeadType {
		return fmt.Errorf("bead %s is not a session (type=%q)", id, b.Type)
	}
	if b.Status == "closed" {
		return nil // already closed
	}

	// If active, kill the runtime session.
	if State(b.Metadata["state"]) == StateActive {
		sessName := b.Metadata["session_name"]
		if sessName == "" {
			sessName = sessionNameFor(id)
		}
		_ = m.sp.Stop(sessName) // best-effort
	}

	return m.store.Close(id)
}

// Get returns info about a single session.
func (m *Manager) Get(id string) (Info, error) {
	b, err := m.store.Get(id)
	if err != nil {
		return Info{}, err
	}
	if b.Type != BeadType {
		return Info{}, fmt.Errorf("bead %s is not a session (type=%q)", id, b.Type)
	}
	return m.infoFromBead(b), nil
}

// List returns all chat sessions, optionally filtered by state and template.
func (m *Manager) List(stateFilter string, templateFilter string) ([]Info, error) {
	all, err := m.store.ListByLabel(LabelSession, 0)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}

	var result []Info
	for _, b := range all {
		if b.Type != BeadType {
			continue
		}
		state := State(b.Metadata["state"])

		// Filter by state.
		if stateFilter != "" && stateFilter != "all" {
			match := false
			for _, s := range strings.Split(stateFilter, ",") {
				switch {
				case s == "closed" && b.Status == "closed":
					match = true
				case s == "open" && b.Status == "open":
					match = true
				case b.Status != "closed" && s == string(state):
					// Only match metadata state for non-closed beads.
					match = true
				}
				if match {
					break
				}
			}
			if !match {
				continue
			}
		} else if stateFilter == "" {
			// Default: exclude closed sessions.
			if b.Status == "closed" {
				continue
			}
		}

		// Filter by template.
		if templateFilter != "" && b.Metadata["template"] != templateFilter {
			continue
		}

		result = append(result, m.infoFromBead(b))
	}
	return result, nil
}

// Peek captures the last N lines of output from the session.
func (m *Manager) Peek(id string, lines int) (string, error) {
	b, err := m.store.Get(id)
	if err != nil {
		return "", err
	}
	if b.Type != BeadType {
		return "", fmt.Errorf("bead %s is not a session", id)
	}
	if b.Status == "closed" || State(b.Metadata["state"]) == StateSuspended {
		return "", errors.New("session is not active")
	}
	sessName := b.Metadata["session_name"]
	if sessName == "" {
		sessName = sessionNameFor(id)
	}
	return m.sp.Peek(sessName, lines)
}

// infoFromBead converts a bead to an Info struct, enriching with runtime state.
func (m *Manager) infoFromBead(b beads.Bead) Info {
	sessName := b.Metadata["session_name"]
	if sessName == "" {
		sessName = sessionNameFor(b.ID)
	}

	state := State(b.Metadata["state"])
	if b.Status == "closed" {
		state = "" // closed beads have no runtime state
	}

	info := Info{
		ID:          b.ID,
		Template:    b.Metadata["template"],
		State:       state,
		Title:       b.Title,
		Provider:    b.Metadata["provider"],
		Command:     b.Metadata["command"],
		WorkDir:     b.Metadata["work_dir"],
		SessionName: sessName,
		CreatedAt:   b.CreatedAt,
	}

	// Enrich with live runtime state if active.
	if state == StateActive && m.sp != nil {
		info.Attached = m.sp.IsAttached(sessName)
		if t, err := m.sp.GetLastActivity(sessName); err == nil && !t.IsZero() {
			info.LastActive = t
		}
	}

	return info
}

// sessionNameFor derives the tmux session name from a bead ID.
// Uses the "s-" prefix to avoid collision with agent sessions.
func sessionNameFor(beadID string) string {
	return "s-" + strings.ReplaceAll(beadID, "/", "--")
}

// mergeEnv merges two env maps, with override taking precedence.
func mergeEnv(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	merged := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range override {
		merged[k] = v
	}
	return merged
}
