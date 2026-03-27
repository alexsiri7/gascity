package sessionlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractTokenTotals_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	lines := []map[string]any{
		{
			"type":    "user",
			"message": map[string]any{"role": "user", "content": "hello"},
		},
		{
			"type": "assistant",
			"message": map[string]any{
				"role":  "assistant",
				"model": "claude-opus-4-5-20251101",
				"usage": map[string]any{
					"input_tokens":                10000,
					"output_tokens":               500,
					"cache_read_input_tokens":     5000,
					"cache_creation_input_tokens": 2000,
				},
			},
		},
		{
			"type":    "user",
			"message": map[string]any{"role": "user", "content": "thanks"},
		},
		{
			"type": "assistant",
			"message": map[string]any{
				"role":  "assistant",
				"model": "claude-opus-4-5-20251101",
				"usage": map[string]any{
					"input_tokens":                15000,
					"output_tokens":               800,
					"cache_read_input_tokens":     8000,
					"cache_creation_input_tokens": 1000,
				},
			},
		},
	}

	writeTokensJSONL(t, path, lines)

	totals, err := ExtractTokenTotals(path)
	if err != nil {
		t.Fatal(err)
	}
	if totals == nil {
		t.Fatal("expected non-nil TokenTotals")
	}

	if totals.InputTokens != 25000 {
		t.Errorf("InputTokens = %d, want 25000", totals.InputTokens)
	}
	if totals.OutputTokens != 1300 {
		t.Errorf("OutputTokens = %d, want 1300", totals.OutputTokens)
	}
	if totals.CacheReadTokens != 13000 {
		t.Errorf("CacheReadTokens = %d, want 13000", totals.CacheReadTokens)
	}
	if totals.CacheCreationTokens != 3000 {
		t.Errorf("CacheCreationTokens = %d, want 3000", totals.CacheCreationTokens)
	}
	if totals.Model != "claude-opus-4-5-20251101" {
		t.Errorf("Model = %q, want %q", totals.Model, "claude-opus-4-5-20251101")
	}
}

func TestExtractTokenTotals_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	totals, err := ExtractTokenTotals(path)
	if err != nil {
		t.Fatal(err)
	}
	if totals != nil {
		t.Error("expected nil TokenTotals for empty file")
	}
}

func TestExtractTokenTotals_NoUsage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	lines := []map[string]any{
		{
			"type": "assistant",
			"message": map[string]any{
				"role":  "assistant",
				"model": "claude-opus-4-5-20251101",
			},
		},
	}

	writeTokensJSONL(t, path, lines)

	totals, err := ExtractTokenTotals(path)
	if err != nil {
		t.Fatal(err)
	}
	if totals != nil {
		t.Error("expected nil TokenTotals when no usage data")
	}
}

func TestExtractTokenTotals_MissingFile(t *testing.T) {
	_, err := ExtractTokenTotals("/nonexistent/path.jsonl")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestExtractTokenTotals_StringWrappedMessage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	inner := `{"role":"assistant","model":"claude-sonnet-4-5-20251101","usage":{"input_tokens":5000,"output_tokens":300}}`
	line := map[string]any{
		"type":    "assistant",
		"message": inner,
	}
	writeTokensJSONL(t, path, []map[string]any{line})

	totals, err := ExtractTokenTotals(path)
	if err != nil {
		t.Fatal(err)
	}
	if totals == nil {
		t.Fatal("expected non-nil TokenTotals")
	}
	if totals.InputTokens != 5000 {
		t.Errorf("InputTokens = %d, want 5000", totals.InputTokens)
	}
	if totals.OutputTokens != 300 {
		t.Errorf("OutputTokens = %d, want 300", totals.OutputTokens)
	}
	if totals.Model != "claude-sonnet-4-5-20251101" {
		t.Errorf("Model = %q, want %q", totals.Model, "claude-sonnet-4-5-20251101")
	}
}

func TestExtractAllTokenTotals_WithSubagents(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "abc123.jsonl")

	mainLines := []map[string]any{
		{
			"type": "assistant",
			"message": map[string]any{
				"role":  "assistant",
				"model": "claude-opus-4-5-20251101",
				"usage": map[string]any{
					"input_tokens":  20000,
					"output_tokens": 1000,
				},
			},
		},
	}
	writeTokensJSONL(t, sessionPath, mainLines)

	subDir := filepath.Join(dir, "abc123", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	subPath := filepath.Join(subDir, "agent-sub1.jsonl")
	subLines := []map[string]any{
		{
			"type": "assistant",
			"message": map[string]any{
				"role":  "assistant",
				"model": "claude-haiku-4-5-20251101",
				"usage": map[string]any{
					"input_tokens":  3000,
					"output_tokens": 200,
				},
			},
		},
	}
	writeTokensJSONL(t, subPath, subLines)

	results, err := ExtractAllTokenTotals(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	if results[0].InputTokens != 20000 {
		t.Errorf("main InputTokens = %d, want 20000", results[0].InputTokens)
	}
	if results[0].OutputTokens != 1000 {
		t.Errorf("main OutputTokens = %d, want 1000", results[0].OutputTokens)
	}
	if results[0].Model != "claude-opus-4-5-20251101" {
		t.Errorf("main Model = %q, want opus", results[0].Model)
	}

	if results[1].InputTokens != 3000 {
		t.Errorf("sub InputTokens = %d, want 3000", results[1].InputTokens)
	}
	if results[1].OutputTokens != 200 {
		t.Errorf("sub OutputTokens = %d, want 200", results[1].OutputTokens)
	}
	if results[1].Model != "claude-haiku-4-5-20251101" {
		t.Errorf("sub Model = %q, want haiku", results[1].Model)
	}
}

func writeTokensJSONL(t *testing.T, path string, lines []map[string]any) {
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
