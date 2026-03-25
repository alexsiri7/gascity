package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gastownhall/gascity/internal/sessionlog"
	"github.com/spf13/cobra"
)

// costsEntry holds aggregated token costs for a single display row.
type costsEntry struct {
	Name              string    `json:"name"`
	Rig               string    `json:"rig,omitempty"`
	Role              string    `json:"role,omitempty"`
	Model             string    `json:"model,omitempty"`
	InputTokens       int64     `json:"input_tokens"`
	CacheReadTokens   int64     `json:"cache_read_tokens"`
	CacheCreateTokens int64     `json:"cache_create_tokens"`
	OutputTokens      int64     `json:"output_tokens"`
	TotalTokens       int64     `json:"total_tokens"`
	Sessions          int       `json:"sessions"`
	LastActive        time.Time `json:"last_active,omitempty"`
}

func newCostsCmd(stdout, stderr io.Writer) *cobra.Command {
	var (
		byRole  bool
		byRig   bool
		today   bool
		week    bool
		window  string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "costs",
		Short: "Show token usage across agents and rigs",
		Long: `Scan Claude JSONL session transcripts and report token usage.

Reads ~/.claude/projects/**/*.jsonl plus any [daemon] observe_paths.
Session names (rig/role) are resolved via the bead store when available.

Time filters apply to the session file's last-modified timestamp.`,
		Example: `  gc costs                    # all sessions, one row per session
  gc costs --today            # sessions active today (UTC)
  gc costs --week             # sessions active in the last 7 days
  gc costs --window 2h        # sessions active in the last 2 hours
  gc costs --by-role          # aggregate by agent role
  gc costs --by-rig           # aggregate by rig
  gc costs --json             # machine-readable JSON
  gc costs record             # record session usage to daily cost log (Stop hook)`,
		RunE: func(_ *cobra.Command, _ []string) error {
			if cmdCosts(byRole, byRig, today, week, window, jsonOut, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&byRole, "by-role", false, "Aggregate by agent role")
	cmd.Flags().BoolVar(&byRig, "by-rig", false, "Aggregate by rig")
	cmd.Flags().BoolVar(&today, "today", false, "Only sessions active today (UTC)")
	cmd.Flags().BoolVar(&week, "week", false, "Only sessions active in the last 7 days")
	cmd.Flags().StringVar(&window, "window", "", "Rolling window (e.g. 1h, 24h, 7d)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output JSON")
	cmd.AddCommand(newCostsRecordCmd(stdout, stderr))
	cmd.AddCommand(newCostsDigestCmd(stdout, stderr))
	return cmd
}

// cmdCosts is the CLI entry point for gc costs.
func cmdCosts(byRole, byRig, today, week bool, window string, jsonOut bool, stdout, stderr io.Writer) int {
	now := time.Now().UTC()

	// Resolve time cutoff.
	cutoff := time.Time{}
	switch {
	case window != "":
		dur, err := time.ParseDuration(window)
		if err != nil {
			fmt.Fprintf(stderr, "gc costs: invalid --window %q: %v\n", window, err) //nolint:errcheck
			return 1
		}
		cutoff = now.Add(-dur)
	case week:
		cutoff = now.Add(-7 * 24 * time.Hour)
	case today:
		y, m, d := now.Date()
		cutoff = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}

	// Load observe_paths from config (best-effort; don't fail on missing city).
	var observePaths []string
	if cityPath, err := resolveCity(); err == nil {
		if cfg, err := loadCityConfig(cityPath); err == nil {
			observePaths = cfg.Daemon.ObservePaths
		}
	}
	searchPaths := sessionlog.MergeSearchPaths(observePaths)

	// Build sessionKey → sessionName index from bead store (best-effort).
	keyToName := buildSessionKeyIndex(stderr)

	// Scan all JSONL files.
	type rawEntry struct {
		uuid         string
		sessionName  string
		mtime        time.Time
		inputTokens  int64
		cacheRead    int64
		cacheCreate  int64
		outputTokens int64
		model        string
	}
	var rows []rawEntry

	for _, root := range searchPaths {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			base := d.Name()
			if filepath.Ext(base) != ".jsonl" {
				return nil
			}
			// Skip subagent files (they live in {uuid}/subagents/agent-*.jsonl);
			// we include them via ExtractAllTokenTotals on the parent.
			if strings.HasSuffix(filepath.Dir(path), "subagents") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			mtime := info.ModTime().UTC()
			if !cutoff.IsZero() && mtime.Before(cutoff) {
				return nil
			}

			// UUID = filename without extension.
			uuid := strings.TrimSuffix(base, ".jsonl")

			// Sum tokens for this session + all subagents.
			totals, err := sessionlog.ExtractAllTokenTotals(path)
			if err != nil || len(totals) == 0 {
				return nil // no usage data — skip
			}
			var in, cr, cc, out int64
			var model string
			for _, t := range totals {
				in += t.InputTokens
				cr += t.CacheReadTokens
				cc += t.CacheCreationTokens
				out += t.OutputTokens
				if t.Model != "" {
					model = t.Model
				}
			}
			if in+cr+cc+out == 0 {
				return nil // no actual usage — skip
			}

			name := keyToName[uuid]
			if name == "" {
				name = uuid
			}
			rows = append(rows, rawEntry{
				uuid:         uuid,
				sessionName:  name,
				mtime:        mtime,
				inputTokens:  in,
				cacheRead:    cr,
				cacheCreate:  cc,
				outputTokens: out,
				model:        model,
			})
			return nil
		})
	}

	if len(rows) == 0 {
		fmt.Fprintln(stdout, "No sessions found.") //nolint:errcheck
		return 0
	}

	// Aggregate rows into display entries.
	type key struct{ k string }
	agg := make(map[string]*costsEntry)
	var order []string

	addOrMerge := func(k, name, rig, role string, r rawEntry) {
		if _, exists := agg[k]; !exists {
			agg[k] = &costsEntry{Name: name, Rig: rig, Role: role}
			order = append(order, k)
		}
		e := agg[k]
		e.InputTokens += r.inputTokens
		e.CacheReadTokens += r.cacheRead
		e.CacheCreateTokens += r.cacheCreate
		e.OutputTokens += r.outputTokens
		e.TotalTokens = e.InputTokens + e.CacheReadTokens + e.CacheCreateTokens + e.OutputTokens
		e.Sessions++
		if r.mtime.After(e.LastActive) {
			e.LastActive = r.mtime
		}
		if r.model != "" {
			e.Model = r.model
		}
	}

	for _, r := range rows {
		rig, role := parseSessionName(r.sessionName)
		switch {
		case byRig:
			addOrMerge(rig, rig, rig, "", r)
		case byRole:
			addOrMerge(role, role, rig, role, r)
		default:
			addOrMerge(r.sessionName, r.sessionName, rig, role, r)
		}
	}

	// Sort by total tokens descending.
	sort.Slice(order, func(i, j int) bool {
		return agg[order[i]].TotalTokens > agg[order[j]].TotalTokens
	})

	// Compute totals row.
	var totEntry costsEntry
	for _, k := range order {
		e := agg[k]
		totEntry.InputTokens += e.InputTokens
		totEntry.CacheReadTokens += e.CacheReadTokens
		totEntry.CacheCreateTokens += e.CacheCreateTokens
		totEntry.OutputTokens += e.OutputTokens
		totEntry.TotalTokens += e.TotalTokens
		totEntry.Sessions += e.Sessions
	}

	if jsonOut {
		out := make([]*costsEntry, 0, len(order))
		for _, k := range order {
			out = append(out, agg[k])
		}
		type jsonResult struct {
			Entries []*costsEntry `json:"entries"`
			Total   costsEntry    `json:"total"`
		}
		totEntry.Name = "TOTAL"
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(jsonResult{Entries: out, Total: totEntry}) //nolint:errcheck
		return 0
	}

	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	header := "NAME\tMODEL\tINPUT\tCACHE_READ\tCACHE_CREATE\tOUTPUT\tTOTAL"
	fmt.Fprintln(w, header) //nolint:errcheck
	for _, k := range order {
		e := agg[k]
		name := e.Name
		if len(name) > 40 {
			name = name[:37] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
			name,
			shortModel(e.Model),
			fmtTokens(e.InputTokens),
			fmtTokens(e.CacheReadTokens),
			fmtTokens(e.CacheCreateTokens),
			fmtTokens(e.OutputTokens),
			fmtTokens(e.TotalTokens),
		)
	}
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
		"TOTAL",
		"-",
		fmtTokens(totEntry.InputTokens),
		fmtTokens(totEntry.CacheReadTokens),
		fmtTokens(totEntry.CacheCreateTokens),
		fmtTokens(totEntry.OutputTokens),
		fmtTokens(totEntry.TotalTokens),
	)
	_ = w.Flush() //nolint:errcheck
	return 0
}

// buildSessionKeyIndex builds a map of sessionUUID → sessionName by scanning
// all session beads in the city store. Best-effort: returns empty map on error.
func buildSessionKeyIndex(stderr io.Writer) map[string]string {
	out := make(map[string]string)
	cityPath, err := resolveCity()
	if err != nil {
		return out
	}
	readDoltPort(cityPath)
	store, err := openCityStoreAt(cityPath)
	if err != nil {
		return out
	}
	beads, err := store.ListByLabel("gc:session", 0)
	if err != nil {
		return out
	}
	for _, b := range beads {
		key := b.Metadata["session_key"]
		name := b.Metadata["session_name"]
		if key != "" && name != "" {
			out[key] = name
		}
	}
	return out
}

// parseSessionName splits a session name into (rig, role).
// Handles both "/" (GC_SESSION_NAME env) and "--" (tmux session name) separators.
// "gascity/polecat-1"  → ("gascity", "polecat-1")
// "gascity--polecat-1" → ("gascity", "polecat-1")
// "mayor"              → ("-", "mayor")
func parseSessionName(name string) (rig, role string) {
	if i := strings.Index(name, "/"); i >= 0 {
		return name[:i], name[i+1:]
	}
	if i := strings.Index(name, "--"); i >= 0 {
		return name[:i], name[i+2:]
	}
	return "-", name
}

// fmtTokens formats a token count with thousands separators.
func fmtTokens(n int64) string {
	if n == 0 {
		return "0"
	}
	s := fmt.Sprintf("%d", n)
	// Insert commas.
	var b strings.Builder
	start := len(s) % 3
	if start == 0 {
		start = 3
	}
	b.WriteString(s[:start])
	for i := start; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// shortModel returns a shortened model name for table display.
func shortModel(model string) string {
	if model == "" {
		return "-"
	}
	// "claude-sonnet-4-6" → "sonnet-4-6"
	model = strings.TrimPrefix(model, "claude-")
	// Truncate to 16 chars.
	if len(model) > 16 {
		model = model[:14] + ".."
	}
	return model
}
