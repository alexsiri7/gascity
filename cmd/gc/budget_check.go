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
// input or output token count exceeds the configured threshold. Returns
// true if a budget is exceeded and the city is being stopped.
//
// searchPaths must already be resolved (e.g. via sessionlog.MergeSearchPaths).
// This runs on every controller reconcile tick. It is read-only and
// zero-cost — no API calls, no agent interaction.
func checkTokenBudget(
	ctx context.Context,
	cfg config.BudgetConfig,
	searchPaths []string,
	now time.Time,
	stdout, stderr io.Writer,
	cancelFn context.CancelFunc,
) bool {
	if cfg.MaxInputTokens <= 0 && cfg.MaxOutputTokens <= 0 {
		return false
	}

	window := cfg.WindowDuration()
	cutoff := now.Add(-window)

	var totalInput, totalOutput int64

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
			totalInput += totals.InputTokens + totals.CacheReadTokens + totals.CacheCreationTokens
			totalOutput += totals.OutputTokens
			return nil
		})
	}

	if cfg.MaxInputTokens > 0 && totalInput > cfg.MaxInputTokens {
		fmt.Fprintf(stderr, //nolint:errcheck // best-effort stderr
			"gc start: [budget] token limit exceeded: %d input tokens in last %s (limit %d) — stopping city\n",
			totalInput, window, cfg.MaxInputTokens)
		fmt.Fprintf(stdout, //nolint:errcheck // best-effort stdout
			"[budget] Token limit exceeded (%d/%d input tokens in last %s). Stopping.\n",
			totalInput, cfg.MaxInputTokens, window)
		_ = os.Stderr.Sync()
		cancelFn()
		return true
	}

	if cfg.MaxOutputTokens > 0 && totalOutput > cfg.MaxOutputTokens {
		fmt.Fprintf(stderr, //nolint:errcheck // best-effort stderr
			"gc start: [budget] token limit exceeded: %d output tokens in last %s (limit %d) — stopping city\n",
			totalOutput, window, cfg.MaxOutputTokens)
		fmt.Fprintf(stdout, //nolint:errcheck // best-effort stdout
			"[budget] Token limit exceeded (%d/%d output tokens in last %s). Stopping.\n",
			totalOutput, cfg.MaxOutputTokens, window)
		_ = os.Stderr.Sync()
		cancelFn()
		return true
	}

	return false
}

// newBudgetChecker returns a function suitable for calling from tick()
// that closes over the cancel func.
func newBudgetChecker(cancelFn context.CancelFunc) func(context.Context, config.BudgetConfig, []string, time.Time, io.Writer, io.Writer) bool {
	return func(ctx context.Context, cfg config.BudgetConfig, observePaths []string, now time.Time, stdout, stderr io.Writer) bool {
		return checkTokenBudget(ctx, cfg, sessionlog.MergeSearchPaths(observePaths), now, stdout, stderr, cancelFn)
	}
}
