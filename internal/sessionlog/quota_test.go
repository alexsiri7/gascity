package sessionlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestScanForQuotaExhausted_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if ScanForQuotaExhausted(path, 3) {
		t.Error("expected false for empty file")
	}
}

func TestScanForQuotaExhausted_NoQuota(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	lines := []map[string]any{
		{"type": "user", "message": map[string]any{"role": "user", "content": "hello"}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": "Hi there!"}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": "How can I help?"}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": "Let me check that."}},
	}
	writeQuotaTestJSONL(t, path, lines)

	if ScanForQuotaExhausted(path, 3) {
		t.Error("expected false when no quota patterns present")
	}
}

func TestScanForQuotaExhausted_AllQuota(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	quotaMsg := "You've hit your limit. Please wait before retrying."
	lines := []map[string]any{
		{"type": "user", "message": map[string]any{"role": "user", "content": "do something"}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": quotaMsg}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": quotaMsg}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": quotaMsg}},
	}
	writeQuotaTestJSONL(t, path, lines)

	if !ScanForQuotaExhausted(path, 3) {
		t.Error("expected true when last 3 assistant entries all contain quota pattern")
	}
}

func TestScanForQuotaExhausted_PartialQuota(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	quotaMsg := "You've hit your limit."
	lines := []map[string]any{
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": "normal response"}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": quotaMsg}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": quotaMsg}},
	}
	writeQuotaTestJSONL(t, path, lines)

	// Last 3 entries: [normal, quota, quota] — not all quota
	if ScanForQuotaExhausted(path, 3) {
		t.Error("expected false when not all last N entries are quota")
	}
}

func TestScanForQuotaExhausted_NotEnoughEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	quotaMsg := "You've hit your limit."
	lines := []map[string]any{
		{"type": "user", "message": map[string]any{"role": "user", "content": "hello"}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": quotaMsg}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": quotaMsg}},
	}
	writeQuotaTestJSONL(t, path, lines)

	// Only 2 assistant entries, tailConsecutive=3 → false
	if ScanForQuotaExhausted(path, 3) {
		t.Error("expected false when fewer assistant entries than tailConsecutive")
	}
}

func TestScanForQuotaExhausted_InterspersedNonAssistant(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	quotaMsg := "You've hit your limit."
	// Non-assistant entries between quota entries should be skipped.
	lines := []map[string]any{
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": quotaMsg}},
		{"type": "user", "message": map[string]any{"role": "user", "content": "retry"}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": quotaMsg}},
		{"type": "system", "subtype": "turn_duration"},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": quotaMsg}},
	}
	writeQuotaTestJSONL(t, path, lines)

	if !ScanForQuotaExhausted(path, 3) {
		t.Error("expected true when last 3 assistant entries all quota (non-assistant entries ignored)")
	}
}

func TestScanForQuotaExhausted_ResultEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	quotaMsg := `{"is_error":true,"error":"You've hit your limit"}`
	lines := []map[string]any{
		{"type": "result", "message": quotaMsg},
		{"type": "result", "message": quotaMsg},
		{"type": "result", "message": quotaMsg},
	}
	writeQuotaTestJSONL(t, path, lines)

	if !ScanForQuotaExhausted(path, 3) {
		t.Error("expected true when result entries contain quota pattern")
	}
}

func TestScanForQuotaExhausted_DefaultTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	quotaMsg := "You've hit your limit."
	lines := []map[string]any{
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": quotaMsg}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": quotaMsg}},
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": quotaMsg}},
	}
	writeQuotaTestJSONL(t, path, lines)

	// tailConsecutive <= 0 should default to 3
	if !ScanForQuotaExhausted(path, 0) {
		t.Error("expected true with default tailConsecutive (0 → 3)")
	}
}

func TestScanForQuotaExhausted_MissingFile(t *testing.T) {
	if ScanForQuotaExhausted("/nonexistent/path.jsonl", 3) {
		t.Error("expected false for missing file")
	}
}

func TestContainsQuotaPattern(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"You've hit your limit", true},
		{"you've hit your limit", true},
		{"exceeded your current quota", true},
		{"insufficient_quota", true},
		{"Rate limit exceeded", true},
		{"rate limit exceeded", true},
		{"normal response", false},
		{"", false},
	}
	for _, tc := range cases {
		got := containsQuotaPattern(tc.input)
		if got != tc.want {
			t.Errorf("containsQuotaPattern(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// writeQuotaTestJSONL writes lines to a JSONL file for quota tests.
func writeQuotaTestJSONL(t *testing.T, path string, lines []map[string]any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // test helper
	enc := json.NewEncoder(f)
	for _, line := range lines {
		if err := enc.Encode(line); err != nil {
			t.Fatal(err)
		}
	}
}
