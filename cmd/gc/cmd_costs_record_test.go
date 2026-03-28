package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/costlog"
)

func TestResolveStopHookContext_Flag(t *testing.T) {
	file, sessionID, cwd := resolveStopHookContext("/path/to/session.jsonl", nil)
	if file != "/path/to/session.jsonl" {
		t.Errorf("got file=%q, want /path/to/session.jsonl", file)
	}
	_ = sessionID
	_ = cwd
}

func TestResolveStopHookContext_StdinJSON(t *testing.T) {
	payload := `{"session_id":"abc123","transcript_path":"/tmp/session.jsonl","cwd":"/work/dir"}` + "\n"
	r := strings.NewReader(payload)

	file, sessionID, cwd := resolveStopHookContext("", r)
	if file != "/tmp/session.jsonl" {
		t.Errorf("file: got %q, want /tmp/session.jsonl", file)
	}
	if sessionID != "abc123" {
		t.Errorf("sessionID: got %q, want abc123", sessionID)
	}
	if cwd != "/work/dir" {
		t.Errorf("cwd: got %q, want /work/dir", cwd)
	}
}

func TestResolveStopHookContext_EmptyStdin(_ *testing.T) {
	r := strings.NewReader("")
	file, sessionID, cwd := resolveStopHookContext("", r)
	_ = file
	_ = sessionID
	_ = cwd
	// No panic — just returns empty strings.
}

func TestReadStopHookPayload_ValidJSON(t *testing.T) {
	payload := `{"session_id":"xyz","transcript_path":"/foo/bar.jsonl","cwd":"/baz"}` + "\n"
	r := strings.NewReader(payload)
	got := readStopHookPayload(r)
	if got == nil {
		t.Fatal("expected payload, got nil")
	}
	if got.SessionID != "xyz" {
		t.Errorf("SessionID: %q", got.SessionID)
	}
	if got.TranscriptPath != "/foo/bar.jsonl" {
		t.Errorf("TranscriptPath: %q", got.TranscriptPath)
	}
}

func TestReadStopHookPayload_InvalidJSON(t *testing.T) {
	r := strings.NewReader("not json\n")
	got := readStopHookPayload(r)
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestReadStopHookPayload_EmptyReader(t *testing.T) {
	r := strings.NewReader("")
	got := readStopHookPayload(r)
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestResolveSessionIdentity(t *testing.T) {
	tests := []struct {
		name        string
		agentEnv    string
		wantSession string
		wantRig     string
		wantRole    string
	}{
		{
			name:        "standard polecat",
			agentEnv:    "gascity/polecat-1",
			wantSession: "gascity/polecat-1",
			wantRig:     "gascity",
			wantRole:    "polecat",
		},
		{
			name:        "mayor no instance",
			agentEnv:    "gascity/mayor",
			wantSession: "gascity/mayor",
			wantRig:     "gascity",
			wantRole:    "mayor",
		},
		{
			name:        "multi-digit instance",
			agentEnv:    "gascity/polecat-12",
			wantSession: "gascity/polecat-12",
			wantRig:     "gascity",
			wantRole:    "polecat",
		},
		{
			name:        "no slash",
			agentEnv:    "localagent",
			wantSession: "localagent",
			wantRig:     "",
			wantRole:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GC_AGENT", tt.agentEnv)
			t.Setenv("GC_SESSION_NAME", "")
			t.Setenv("GC_ALIAS", "")

			session, rig, role := resolveSessionIdentity()
			if session != tt.wantSession {
				t.Errorf("session: got %q, want %q", session, tt.wantSession)
			}
			if rig != tt.wantRig {
				t.Errorf("rig: got %q, want %q", rig, tt.wantRig)
			}
			if role != tt.wantRole {
				t.Errorf("role: got %q, want %q", role, tt.wantRole)
			}
		})
	}
}

func TestIsDigits(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"123", true},
		{"0", true},
		{"", false},
		{"12a", false},
		{"abc", false},
	}
	for _, c := range cases {
		if got := isDigits(c.s); got != c.want {
			t.Errorf("isDigits(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

func TestCmdCostsRecord_WritesRecord(t *testing.T) {
	// Create a temp city with a session JSONL file.
	cityDir := t.TempDir()
	// Create the .gc runtime directory so resolveCity succeeds.
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a minimal city.toml.
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte("[city]\nname = \"test\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a session JSONL file with token usage.
	sessionFile := filepath.Join(t.TempDir(), "session.jsonl")
	entry := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role":  "assistant",
			"model": "claude-sonnet-4-6",
			"usage": map[string]any{
				"input_tokens":                123,
				"output_tokens":               45,
				"cache_read_input_tokens":     10,
				"cache_creation_input_tokens": 5,
			},
		},
	}
	data, _ := json.Marshal(entry)
	data = append(data, '\n')
	if err := os.WriteFile(sessionFile, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Build Stop hook JSON payload.
	payload := stopHookPayload{
		SessionID:      "test-session",
		TranscriptPath: sessionFile,
		CWD:            cityDir,
	}
	payloadJSON, _ := json.Marshal(payload)

	// Set up env.
	t.Setenv("GC_AGENT", "gascity/polecat-1")
	t.Setenv("GC_CITY", cityDir)

	var stdout, stderr bytes.Buffer
	cmdCostsRecord("", bytes.NewReader(append(payloadJSON, '\n')), &stdout, &stderr)

	if stderr.Len() > 0 {
		t.Errorf("unexpected stderr: %s", stderr.String())
	}

	// Verify the cost record was written.
	costsDir := filepath.Join(cityDir, ".gc", "runtime", "costs")
	records, err := costlog.ReadDay(costsDir, time.Now().UTC())
	if err != nil {
		t.Fatalf("ReadDay: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}

	rec := records[0]
	if rec.Session != "gascity/polecat-1" {
		t.Errorf("Session: %q", rec.Session)
	}
	if rec.Rig != "gascity" {
		t.Errorf("Rig: %q", rec.Rig)
	}
	if rec.Role != "polecat" {
		t.Errorf("Role: %q", rec.Role)
	}
	if rec.Model != "claude-sonnet-4-6" {
		t.Errorf("Model: %q", rec.Model)
	}
	if rec.InputTokens != 123 {
		t.Errorf("InputTokens: %d", rec.InputTokens)
	}
	if rec.OutputTokens != 45 {
		t.Errorf("OutputTokens: %d", rec.OutputTokens)
	}
	if rec.CacheRead != 10 {
		t.Errorf("CacheRead: %d", rec.CacheRead)
	}
	if rec.CacheCreation != 5 {
		t.Errorf("CacheCreation: %d", rec.CacheCreation)
	}
}

func TestCmdCostsRecord_NoSessionFile(t *testing.T) {
	cityDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte("[city]\nname = \"test\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GC_CITY", cityDir)
	t.Setenv("GC_AGENT", "gascity/polecat-1")

	// Empty stdin — no session file resolvable.
	var stdout, stderr bytes.Buffer
	cmdCostsRecord("", strings.NewReader(""), &stdout, &stderr)

	// Should not panic; stderr may contain a message about missing session file.
	// Costs directory should not be created.
	costsDir := filepath.Join(cityDir, ".gc", "runtime", "costs")
	if _, err := os.Stat(costsDir); err == nil {
		t.Errorf("costs dir should not exist when no session file found")
	}
}

func TestCmdCostsRecord_ZeroTokens(t *testing.T) {
	cityDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte("[city]\nname = \"test\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Session file with no usage data.
	sessionFile := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(sessionFile, []byte(`{"type":"system","content":"hello"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GC_CITY", cityDir)
	t.Setenv("GC_AGENT", "gascity/polecat-1")

	payload := stopHookPayload{TranscriptPath: sessionFile}
	payloadJSON, _ := json.Marshal(payload)

	var stdout, stderr bytes.Buffer
	cmdCostsRecord("", bytes.NewReader(append(payloadJSON, '\n')), &stdout, &stderr)

	// Nothing written — no record for zero-token session.
	costsDir := filepath.Join(cityDir, ".gc", "runtime", "costs")
	records, _ := costlog.ReadDay(costsDir, time.Now().UTC())
	if len(records) != 0 {
		t.Errorf("got %d records for zero-token session, want 0", len(records))
	}
}
