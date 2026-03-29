package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
)

// writeTestJSONL writes a minimal Claude JSONL session file with the given
// input token count so the budget checker can read it.
func writeTestJSONL(t *testing.T, dir, name string, inputTokens int64) string {
	t.Helper()
	return writeTestJSONLFull(t, dir, name, inputTokens, 10)
}

// writeTestJSONLFull writes a minimal Claude JSONL session file with the given
// input and output token counts so the budget checker can read it.
func writeTestJSONLFull(t *testing.T, dir, name string, inputTokens, outputTokens int64) string {
	t.Helper()
	type usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	}
	type msg struct {
		Model string `json:"model"`
		Usage *usage `json:"usage"`
	}
	type entry struct {
		Type    string `json:"type"`
		Message *msg   `json:"message"`
	}
	e := entry{
		Type: "assistant",
		Message: &msg{
			Model: "claude-test",
			Usage: &usage{InputTokens: inputTokens, OutputTokens: outputTokens},
		},
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckTokenBudget_Disabled(t *testing.T) {
	cfg := config.BudgetConfig{MaxInputTokens: 0}
	cancelled := false
	cancel := func() { cancelled = true }
	exceeded := checkTokenBudget(context.Background(), cfg, nil, time.Now(), &bytes.Buffer{}, &bytes.Buffer{}, cancel)
	if exceeded || cancelled {
		t.Fatal("expected no-op when MaxInputTokens=0")
	}
}

func TestCheckTokenBudget_UnderLimit(t *testing.T) {
	dir := t.TempDir()
	writeTestJSONL(t, dir, "session.jsonl", 100)

	cfg := config.BudgetConfig{MaxInputTokens: 1000, Window: "1h"}
	cancelled := false
	cancel := func() { cancelled = true }
	exceeded := checkTokenBudget(context.Background(), cfg, []string{dir}, time.Now(), &bytes.Buffer{}, &bytes.Buffer{}, cancel)
	if exceeded || cancelled {
		t.Fatal("expected no action when tokens under limit")
	}
}

func TestCheckTokenBudget_ExceedsLimit(t *testing.T) {
	dir := t.TempDir()
	writeTestJSONL(t, dir, "session.jsonl", 5000)

	cfg := config.BudgetConfig{MaxInputTokens: 1000, Window: "1h"}
	cancelled := false
	cancel := func() { cancelled = true }
	var stdout, stderr bytes.Buffer
	exceeded := checkTokenBudget(context.Background(), cfg, []string{dir}, time.Now(), &stdout, &stderr, cancel)
	if !exceeded {
		t.Fatal("expected budget exceeded")
	}
	if !cancelled {
		t.Fatal("expected cancel to be called")
	}
	if stderr.Len() == 0 {
		t.Fatal("expected message on stderr")
	}
}

func TestCheckTokenBudget_OldFileIgnored(t *testing.T) {
	dir := t.TempDir()
	path := writeTestJSONL(t, dir, "session.jsonl", 5000)

	// Back-date the file beyond the window.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	cfg := config.BudgetConfig{MaxInputTokens: 1000, Window: "1h"}
	cancelled := false
	cancel := func() { cancelled = true }
	exceeded := checkTokenBudget(context.Background(), cfg, []string{dir}, time.Now(), &bytes.Buffer{}, &bytes.Buffer{}, cancel)
	if exceeded || cancelled {
		t.Fatal("expected old file to be ignored")
	}
}

func TestCheckTokenBudget_OutputDisabled(t *testing.T) {
	cfg := config.BudgetConfig{MaxOutputTokens: 0}
	cancelled := false
	cancel := func() { cancelled = true }
	exceeded := checkTokenBudget(context.Background(), cfg, nil, time.Now(), &bytes.Buffer{}, &bytes.Buffer{}, cancel)
	if exceeded || cancelled {
		t.Fatal("expected no-op when MaxOutputTokens=0")
	}
}

func TestCheckTokenBudget_OutputUnderLimit(t *testing.T) {
	dir := t.TempDir()
	writeTestJSONLFull(t, dir, "session.jsonl", 100, 50)

	cfg := config.BudgetConfig{MaxOutputTokens: 1000, Window: "1h"}
	cancelled := false
	cancel := func() { cancelled = true }
	exceeded := checkTokenBudget(context.Background(), cfg, []string{dir}, time.Now(), &bytes.Buffer{}, &bytes.Buffer{}, cancel)
	if exceeded || cancelled {
		t.Fatal("expected no action when output tokens under limit")
	}
}

func TestCheckTokenBudget_OutputExceedsLimit(t *testing.T) {
	dir := t.TempDir()
	writeTestJSONLFull(t, dir, "session.jsonl", 100, 5000)

	cfg := config.BudgetConfig{MaxOutputTokens: 1000, Window: "1h"}
	cancelled := false
	cancel := func() { cancelled = true }
	var stdout, stderr bytes.Buffer
	exceeded := checkTokenBudget(context.Background(), cfg, []string{dir}, time.Now(), &stdout, &stderr, cancel)
	if !exceeded {
		t.Fatal("expected budget exceeded")
	}
	if !cancelled {
		t.Fatal("expected cancel to be called")
	}
	if stderr.Len() == 0 {
		t.Fatal("expected message on stderr")
	}
	if stdout.Len() == 0 {
		t.Fatal("expected message on stdout")
	}
}

func TestCheckTokenBudget_OutputLimitNotTriggeredByInput(t *testing.T) {
	dir := t.TempDir()
	// High input tokens but low output tokens — only MaxOutputTokens is set.
	writeTestJSONLFull(t, dir, "session.jsonl", 500000, 50)

	cfg := config.BudgetConfig{MaxOutputTokens: 1000, Window: "1h"}
	cancelled := false
	cancel := func() { cancelled = true }
	exceeded := checkTokenBudget(context.Background(), cfg, []string{dir}, time.Now(), &bytes.Buffer{}, &bytes.Buffer{}, cancel)
	if exceeded || cancelled {
		t.Fatal("expected no action: output tokens under limit even though input is high")
	}
}
