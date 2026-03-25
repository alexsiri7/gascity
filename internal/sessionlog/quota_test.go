package sessionlog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectQuotaExhaustion_NoPattern(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := `{"type":"user","message":{"role":"user","content":"hello"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I can help with that."}]}}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if DetectQuotaExhaustion(path) {
		t.Error("expected false for normal session, got true")
	}
}

func TestDetectQuotaExhaustion_HitYourLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := `{"type":"result","is_error":true,"result":"Error: You've hit your limit for this period."}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !DetectQuotaExhaustion(path) {
		t.Error("expected true for quota error pattern, got false")
	}
}

func TestDetectQuotaExhaustion_RateLimitExceeded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := `{"type":"result","is_error":true,"result":"rate limit exceeded — please try again later"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !DetectQuotaExhaustion(path) {
		t.Error("expected true for rate limit pattern, got false")
	}
}

func TestDetectQuotaExhaustion_QuotaExceeded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := `{"type":"system","subtype":"error","message":"quota exceeded for account"}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !DetectQuotaExhaustion(path) {
		t.Error("expected true for quota exceeded pattern, got false")
	}
}

func TestDetectQuotaExhaustion_PatternInMiddle(t *testing.T) {
	// Pattern appears earlier in the file; recent content is normal.
	// Since we tail the last N bytes, if the file is small enough the
	// pattern is still detected.
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := `{"type":"result","is_error":true,"result":"You've hit your limit"}
{"type":"user","message":{"role":"user","content":"continue"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"OK."}]}}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Small file fits within tailChunkSize — pattern is detected.
	if !DetectQuotaExhaustion(path) {
		t.Error("expected true when pattern appears in tail window, got false")
	}
}

func TestDetectQuotaExhaustion_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if DetectQuotaExhaustion(path) {
		t.Error("expected false for empty file, got true")
	}
}

func TestDetectQuotaExhaustion_FileNotFound(t *testing.T) {
	if DetectQuotaExhaustion("/nonexistent/path/session.jsonl") {
		t.Error("expected false for missing file, got true")
	}
}

func TestDetectQuotaExhaustion_CaseVariants(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"lowercase you've", `{"type":"result","result":"you've hit your limit"}`, true},
		{"uppercase Rate", `{"type":"result","result":"Rate limit exceeded"}`, true},
		{"uppercase Quota", `{"type":"result","result":"Quota exceeded here"}`, true},
		{"account rate limited", `{"type":"result","result":"account has been rate limited"}`, true},
		{"unrelated error", `{"type":"result","is_error":true,"result":"permission denied"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "session.jsonl")
			if err := os.WriteFile(path, []byte(tt.content+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			got := DetectQuotaExhaustion(path)
			if got != tt.want {
				t.Errorf("DetectQuotaExhaustion = %v, want %v", got, tt.want)
			}
		})
	}
}
