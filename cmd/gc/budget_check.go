package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/sessionlog"
)

// checkTokenBudget reads token usage from Claude JSONL session files
// modified within the configured window and stops the city if the total
// input token count exceeds the threshold. Returns true if the budget is
// exceeded and the city is being stopped.
//
// This runs on every controller reconcile tick. It is read-only and
// zero-cost — no API calls, no agent interaction.
func checkTokenBudget(
	ctx context.Context,
	cfg config.BudgetConfig,
	observePaths []string,
	now time.Time,
	stdout, stderr io.Writer,
	cancelFn context.CancelFunc,
) bool {
	if cfg.MaxInputTokens <= 0 {
		return false
	}

	window := cfg.WindowDuration()
	cutoff := now.Add(-window)

	searchPaths := sessionlog.MergeSearchPaths(observePaths)
	var total int64

	for _, root := range searchPaths {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".jsonl" {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.ModTime().Before(cutoff) {
				return nil
			}
			totals, err := sessionlog.ExtractTokenTotals(path)
			if err != nil || totals == nil {
				return nil
			}
			// Input tokens = raw input + cache reads + cache writes.
			total += totals.InputTokens + totals.CacheReadTokens + totals.CacheCreationTokens
			return nil
		})
	}

	if total <= cfg.MaxInputTokens {
		return false
	}

	fmt.Fprintf(stderr, //nolint:errcheck // best-effort stderr
		"gc start: [budget] token limit exceeded: %d input tokens in last %s (limit %d) — stopping city\n",
		total, window, cfg.MaxInputTokens)
	fmt.Fprintf(stdout, //nolint:errcheck // best-effort stdout
		"[budget] Token limit exceeded (%d/%d input tokens in last %s). Stopping.\n",
		total, cfg.MaxInputTokens, window)

	// Record to stderr before cancel so the message is visible.
	_ = os.Stderr.Sync()
	cancelFn()
	return true
}

// newBudgetChecker returns a function suitable for calling from tick()
// that closes over the cancel func.
func newBudgetChecker(cancelFn context.CancelFunc) func(context.Context, config.BudgetConfig, []string, time.Time, io.Writer, io.Writer) bool {
	return func(ctx context.Context, cfg config.BudgetConfig, observePaths []string, now time.Time, stdout, stderr io.Writer) bool {
		return checkTokenBudget(ctx, cfg, observePaths, now, stdout, stderr, cancelFn)
	}
}
