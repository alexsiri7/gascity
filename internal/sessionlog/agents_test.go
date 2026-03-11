package sessionlog

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTestFile is a test helper that writes a file and fails the test on error.
func writeTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeTestFile(%s): %v", path, err)
	}
}

func TestFindAgentFiles(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "session-abc.jsonl")
	writeTestFile(t, parentPath, `{"uuid":"u1","type":"user"}`+"\n")

	files := FindAgentFiles(parentPath)
	if len(files) != 0 {
		t.Fatalf("expected 0 agent files, got %d", len(files))
	}

	writeTestFile(t, filepath.Join(dir, "agent-def.jsonl"), `{"uuid":"a1"}`+"\n")
	writeTestFile(t, filepath.Join(dir, "agent-ghi.jsonl"), `{"uuid":"a2"}`+"\n")
	writeTestFile(t, filepath.Join(dir, "other.jsonl"), `{"uuid":"o1"}`+"\n")

	files = FindAgentFiles(parentPath)
	if len(files) != 2 {
		t.Fatalf("expected 2 agent files, got %d: %v", len(files), files)
	}
}

func TestAgentIDFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/tmp/agent-abc123.jsonl", "abc123"},
		{"/tmp/agent-uuid-with-dashes.jsonl", "uuid-with-dashes"},
		{"/tmp/session-abc.jsonl", ""},
		{"/tmp/agent-.jsonl", ""},
	}
	for _, tt := range tests {
		got := agentIDFromPath(tt.path)
		if got != tt.want {
			t.Errorf("agentIDFromPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestExtractParentToolUseID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-test.jsonl")
	content := `{"uuid":"u1","type":"system","parentToolUseId":"toolu_abc123"}` + "\n" +
		`{"uuid":"u2","type":"user","message":{"role":"user","content":"hello"}}` + "\n"
	writeTestFile(t, path, content)

	got := extractParentToolUseID(path)
	if got != "toolu_abc123" {
		t.Errorf("extractParentToolUseID = %q, want %q", got, "toolu_abc123")
	}
}

func TestExtractParentToolUseID_Missing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-test.jsonl")
	writeTestFile(t, path, `{"uuid":"u1","type":"user"}`+"\n")

	got := extractParentToolUseID(path)
	if got != "" {
		t.Errorf("extractParentToolUseID = %q, want empty", got)
	}
}

func TestFindAgentMappings(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "session-abc.jsonl")
	writeTestFile(t, parentPath, `{"uuid":"u1","type":"user"}`+"\n")

	writeTestFile(t, filepath.Join(dir, "agent-def.jsonl"),
		`{"uuid":"a1","type":"system","parentToolUseId":"toolu_111"}`+"\n")
	writeTestFile(t, filepath.Join(dir, "agent-ghi.jsonl"),
		`{"uuid":"a2","type":"system","parentToolUseId":"toolu_222"}`+"\n")

	mappings, err := FindAgentMappings(parentPath)
	if err != nil {
		t.Fatalf("FindAgentMappings: %v", err)
	}
	if len(mappings) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(mappings))
	}

	found := make(map[string]string)
	for _, m := range mappings {
		found[m.AgentID] = m.ParentToolUseID
	}
	if found["def"] != "toolu_111" {
		t.Errorf("agent def: want toolu_111, got %q", found["def"])
	}
	if found["ghi"] != "toolu_222" {
		t.Errorf("agent ghi: want toolu_222, got %q", found["ghi"])
	}
}

func TestReadAgentSession(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "session-abc.jsonl")
	writeTestFile(t, parentPath, `{"uuid":"u1","type":"user"}`+"\n")

	agentContent := `{"uuid":"a1","type":"system","parentToolUseId":"toolu_111"}` + "\n" +
		`{"uuid":"a2","parentUuid":"a1","type":"user","message":{"role":"user","content":"do task"}}` + "\n" +
		`{"uuid":"a3","parentUuid":"a2","type":"assistant","message":{"role":"assistant","content":"done"}}` + "\n" +
		`{"uuid":"a4","parentUuid":"a3","type":"result","message":{"role":"result"}}` + "\n"
	writeTestFile(t, filepath.Join(dir, "agent-myagent.jsonl"), agentContent)

	sess, err := ReadAgentSession(parentPath, "myagent")
	if err != nil {
		t.Fatalf("ReadAgentSession: %v", err)
	}
	if sess.Status != AgentStatusCompleted {
		t.Errorf("status = %q, want %q", sess.Status, AgentStatusCompleted)
	}
	if len(sess.Messages) != 4 {
		t.Errorf("messages = %d, want 4", len(sess.Messages))
	}
}

func TestReadAgentSession_Running(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "session-abc.jsonl")
	writeTestFile(t, parentPath, `{"uuid":"u1","type":"user"}`+"\n")

	agentContent := `{"uuid":"a1","type":"system","parentToolUseId":"toolu_111"}` + "\n" +
		`{"uuid":"a2","parentUuid":"a1","type":"assistant","message":{"role":"assistant","content":"working..."}}` + "\n"
	writeTestFile(t, filepath.Join(dir, "agent-running.jsonl"), agentContent)

	sess, err := ReadAgentSession(parentPath, "running")
	if err != nil {
		t.Fatalf("ReadAgentSession: %v", err)
	}
	if sess.Status != AgentStatusRunning {
		t.Errorf("status = %q, want %q", sess.Status, AgentStatusRunning)
	}
}

func TestReadAgentSession_Failed(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "session-abc.jsonl")
	writeTestFile(t, parentPath, `{"uuid":"u1","type":"user"}`+"\n")

	agentContent := `{"uuid":"a1","type":"system","parentToolUseId":"toolu_111"}` + "\n" +
		`{"uuid":"a2","parentUuid":"a1","type":"result","message":{"is_error":true}}` + "\n"
	writeTestFile(t, filepath.Join(dir, "agent-failed.jsonl"), agentContent)

	sess, err := ReadAgentSession(parentPath, "failed")
	if err != nil {
		t.Fatalf("ReadAgentSession: %v", err)
	}
	if sess.Status != AgentStatusFailed {
		t.Errorf("status = %q, want %q", sess.Status, AgentStatusFailed)
	}
}

func TestReadAgentSession_NotFound(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "session-abc.jsonl")
	writeTestFile(t, parentPath, `{"uuid":"u1","type":"user"}`+"\n")

	_, err := ReadAgentSession(parentPath, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestValidateAgentID(t *testing.T) {
	tests := []struct {
		id      string
		wantErr bool
	}{
		{"abc123", false},
		{"uuid-with-dashes", false},
		{"", true},
		{"../etc/passwd", true},
		{"foo/bar", true},
		{"foo\\bar", true},
		{"..%2f..%2fetc", true}, // contains ".." literal
		{"valid-agent-id", false},
	}
	for _, tt := range tests {
		err := ValidateAgentID(tt.id)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateAgentID(%q) err=%v, wantErr=%v", tt.id, err, tt.wantErr)
		}
	}
}

func TestReadAgentSession_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "session-abc.jsonl")
	writeTestFile(t, parentPath, `{"uuid":"u1","type":"user"}`+"\n")

	// Create a file outside the session directory that could be targeted.
	outsideDir := t.TempDir()
	writeTestFile(t, filepath.Join(outsideDir, "agent-secret.jsonl"),
		`{"uuid":"s1","type":"system"}`+"\n")

	traversalIDs := []string{
		"../../../etc/passwd",
		"foo/bar",
		"..\\windows\\system32",
	}
	for _, id := range traversalIDs {
		_, err := ReadAgentSession(parentPath, id)
		if err == nil {
			t.Errorf("ReadAgentSession with traversal ID %q should fail", id)
		}
	}
}

func TestReadAgentSession_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "session-abc.jsonl")
	writeTestFile(t, parentPath, `{"uuid":"u1","type":"user"}`+"\n")

	// Write a corrupt agent file — parseFile skips malformed lines,
	// so this produces an empty transcript with "pending" status.
	writeTestFile(t, filepath.Join(dir, "agent-corrupt.jsonl"), "not json at all\n")

	sess, err := ReadAgentSession(parentPath, "corrupt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sess.Status != AgentStatusPending {
		t.Errorf("status = %q, want %q (all lines were unparseable)", sess.Status, AgentStatusPending)
	}
}

func TestInferAgentStatus(t *testing.T) {
	tests := []struct {
		name     string
		messages []*Entry
		want     AgentStatus
	}{
		{"empty", nil, AgentStatusPending},
		{"no result", []*Entry{{Type: "assistant"}}, AgentStatusRunning},
		{"completed", []*Entry{{Type: "assistant"}, {Type: "result", Message: []byte(`{}`)}}, AgentStatusCompleted},
		{"failed", []*Entry{{Type: "assistant"}, {Type: "result", Message: []byte(`{"is_error":true}`)}}, AgentStatusFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferAgentStatus(tt.messages)
			if got != tt.want {
				t.Errorf("inferAgentStatus = %q, want %q", got, tt.want)
			}
		})
	}
}
