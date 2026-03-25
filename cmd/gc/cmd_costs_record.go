package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/citylayout"
	"github.com/gastownhall/gascity/internal/costlog"
	"github.com/gastownhall/gascity/internal/sessionlog"
	"github.com/spf13/cobra"
)

// stopHookPayload is the JSON payload that Claude Code sends to Stop hooks on stdin.
type stopHookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
}

// newCostsRecordCmd creates the "gc costs record" subcommand.
func newCostsRecordCmd(stdout, stderr io.Writer) *cobra.Command {
	var sessionFile string
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Record per-session token usage to the daily cost log (Stop hook)",
		Long: `Reads a session's JSONL transcript, sums token usage across the
main session and all subagent files, and appends a record to the
daily cost log at {city}/.gc/runtime/costs/YYYY-MM-DD.jsonl.

Designed for use as a Stop hook. The session file path is resolved from:
  1. --session-file flag
  2. transcript_path in the JSON payload on stdin (Claude Code Stop hook)
  3. $GC_SESSION_ID / $CLAUDE_SESSION_ID + working directory lookup

Always exits 0 — failures are logged to stderr but do not block the hook.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			cmdCostsRecord(sessionFile, os.Stdin, stdout, stderr)
			return nil // always exit 0 — best-effort hook
		},
	}
	cmd.Flags().StringVar(&sessionFile, "session-file", "", "Path to session JSONL file (overrides stdin/env lookup)")
	return cmd
}

// cmdCostsRecord is the pure logic for "gc costs record".
// It is best-effort: errors are logged to stderr but never returned.
func cmdCostsRecord(sessionFileFlag string, stdin io.Reader, _, stderr io.Writer) {
	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc costs record: cannot resolve city: %v\n", err) //nolint:errcheck
		return
	}

	sessionFile, sessionID, cwd := resolveStopHookContext(sessionFileFlag, stdin)
	if sessionFile == "" {
		sessionFile = findSessionFileFromContext(sessionID, cwd)
	}
	if sessionFile == "" {
		fmt.Fprintf(stderr, "gc costs record: cannot find session file (no transcript_path, session_id, or session key)\n") //nolint:errcheck
		return
	}

	totals, err := sessionlog.ExtractAllTokenTotals(sessionFile)
	if err != nil {
		fmt.Fprintf(stderr, "gc costs record: extracting token totals from %s: %v\n", sessionFile, err) //nolint:errcheck
		return
	}
	if len(totals) == 0 {
		return // no usage data — nothing to record
	}

	// Sum totals across main session and all subagents.
	var (
		inputTokens   int64
		cacheRead     int64
		cacheCreation int64
		outputTokens  int64
		model         string
	)
	for _, t := range totals {
		inputTokens += t.InputTokens
		cacheRead += t.CacheReadTokens
		cacheCreation += t.CacheCreationTokens
		outputTokens += t.OutputTokens
		if model == "" && t.Model != "" {
			model = t.Model
		}
	}

	session, rig, role := resolveSessionIdentity()

	rec := costlog.Record{
		Timestamp:     time.Now().UTC(),
		Session:       session,
		Rig:           rig,
		Role:          role,
		Model:         model,
		InputTokens:   inputTokens,
		CacheRead:     cacheRead,
		CacheCreation: cacheCreation,
		OutputTokens:  outputTokens,
	}

	costsDir := citylayout.RuntimePath(cityPath, "runtime", "costs")
	if err := costlog.AppendRecord(costsDir, rec); err != nil {
		fmt.Fprintf(stderr, "gc costs record: writing cost record: %v\n", err) //nolint:errcheck
	}
}

// resolveStopHookContext returns the session file path, session ID, and cwd
// by examining the flag value, then the JSON payload on stdin.
func resolveStopHookContext(sessionFileFlag string, stdin io.Reader) (sessionFile, sessionID, cwd string) {
	if sessionFileFlag != "" {
		return sessionFileFlag, "", ""
	}

	// Try to read the Stop hook JSON payload from stdin (non-blocking).
	payload := readStopHookPayload(stdin)
	if payload != nil {
		return payload.TranscriptPath, payload.SessionID, payload.CWD
	}

	// Fall back to env vars.
	if id := os.Getenv("GC_SESSION_ID"); id != "" {
		sessionID = id
	} else if id := os.Getenv("CLAUDE_SESSION_ID"); id != "" {
		sessionID = id
	}
	return "", sessionID, ""
}

// readStopHookPayload reads and parses the Claude Code Stop hook JSON payload
// from stdin. Returns nil if stdin is a terminal or the payload is unreadable.
func readStopHookPayload(r io.Reader) *stopHookPayload {
	if r == nil {
		return nil
	}
	// If r is *os.File, check if it's a terminal.
	if f, ok := r.(*os.File); ok {
		if stat, err := f.Stat(); err == nil {
			if (stat.Mode() & os.ModeCharDevice) != 0 {
				return nil // interactive terminal — no payload
			}
		}
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	if !scanner.Scan() {
		return nil
	}
	line := strings.TrimSpace(scanner.Text())
	if line == "" {
		return nil
	}
	var payload stopHookPayload
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return nil
	}
	return &payload
}

// findSessionFileFromContext resolves a session file using a session ID and
// optional working directory. Falls back to the current working directory.
func findSessionFileFromContext(sessionID, cwd string) string {
	searchPaths := sessionlog.DefaultSearchPaths()

	// Try city observe_paths (best-effort).
	if cityPath, err := resolveCity(); err == nil {
		if cfg, err := loadCityConfig(cityPath); err == nil {
			searchPaths = sessionlog.MergeSearchPaths(cfg.Daemon.ObservePaths)
		}
	}

	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	}
	if cwd != "" {
		if abs, err := filepath.Abs(cwd); err == nil {
			cwd = abs
		}
	}

	if sessionID != "" && cwd != "" {
		if path := sessionlog.FindSessionFileByID(searchPaths, cwd, sessionID); path != "" {
			return path
		}
	}
	if cwd != "" {
		return sessionlog.FindSessionFile(searchPaths, cwd)
	}
	return ""
}

// resolveSessionIdentity returns the session name, rig, and role for the
// current agent. Session comes from GC_AGENT or GC_SESSION_NAME.
// Rig and role are parsed from the agent name.
func resolveSessionIdentity() (session, rig, role string) {
	session = os.Getenv("GC_AGENT")
	if session == "" {
		session = os.Getenv("GC_SESSION_NAME")
	}
	if session == "" {
		session = os.Getenv("GC_ALIAS")
	}
	if session == "" {
		return "", "", ""
	}

	// Parse "rig/role-instance" → rig="rig", role="role".
	// GC_AGENT is typically "gascity/polecat-1".
	parts := strings.SplitN(session, "/", 2)
	if len(parts) == 2 {
		rig = parts[0]
		// Strip trailing instance suffix "-N" from role.
		roleWithInstance := parts[1]
		if idx := strings.LastIndex(roleWithInstance, "-"); idx > 0 {
			if isDigits(roleWithInstance[idx+1:]) {
				role = roleWithInstance[:idx]
			} else {
				role = roleWithInstance
			}
		} else {
			role = roleWithInstance
		}
	}
	return session, rig, role
}

// isDigits reports whether s contains only ASCII digit characters.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
