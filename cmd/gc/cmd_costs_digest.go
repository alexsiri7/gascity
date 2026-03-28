package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/citylayout"
	"github.com/gastownhall/gascity/internal/costlog"
	"github.com/spf13/cobra"
)

// newCostsDigestCmd creates the "gc costs digest" subcommand.
func newCostsDigestCmd(stdout, stderr io.Writer) *cobra.Command {
	var (
		flagToday bool
		flagWeek  bool
		flagJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "digest",
		Short: "Show a daily/weekly token usage summary",
		Long: `Reads cost log files written by the Stop hook
(~/.gc/runtime/costs/YYYY-MM-DD.jsonl) and prints a formatted summary.

--today (default): reads today's log
--week: reads the last 7 daily files

Output groups usage By Role and By Rig, with totals at the bottom.
Use --json to get machine-readable output.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return cmdCostsDigest(flagToday, flagWeek, flagJSON, stdout, stderr)
		},
	}
	cmd.Flags().BoolVar(&flagToday, "today", false, "Show today's token usage (default)")
	cmd.Flags().BoolVar(&flagWeek, "week", false, "Show the last 7 days of token usage")
	cmd.Flags().BoolVar(&flagJSON, "json", false, "Output as JSON")
	return cmd
}

// digestDay is a summary for a single day (used in JSON output).
type digestDay struct {
	Date        string          `json:"date"`
	ByRole      []digestRoleRow `json:"by_role"`
	ByRig       []digestRigRow  `json:"by_rig"`
	TotalInput  int64           `json:"total_input_tokens"`
	TotalOutput int64           `json:"total_output_tokens"`
}

// digestRoleRow is per-role token totals.
type digestRoleRow struct {
	Role         string `json:"role"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// digestRigRow is per-rig token totals.
type digestRigRow struct {
	Rig         string `json:"rig"`
	InputTokens int64  `json:"input_tokens"`
}

// cmdCostsDigest is the pure implementation of "gc costs digest".
func cmdCostsDigest(_ bool, flagWeek, flagJSON bool, stdout, stderr io.Writer) error {
	cityPath, err := resolveCity()
	if err != nil {
		return fmt.Errorf("cannot resolve city: %w", err)
	}
	costsDir := citylayout.RuntimePath(cityPath, "runtime", "costs")

	// Build the list of dates to read.
	now := time.Now().UTC()
	var dates []time.Time
	if flagWeek {
		for i := 6; i >= 0; i-- {
			dates = append(dates, now.AddDate(0, 0, -i))
		}
	} else {
		// Default to today (--today is also the default behavior).
		dates = []time.Time{now}
	}

	// Read all records for the requested date range.
	var allRecords []costlog.Record
	for _, d := range dates {
		recs, err := costlog.ReadDay(costsDir, d)
		if err != nil {
			fmt.Fprintf(stderr, "gc costs digest: reading %s: %v\n", d.Format("2006-01-02"), err) //nolint:errcheck
			continue
		}
		allRecords = append(allRecords, recs...)
	}

	if len(allRecords) == 0 {
		if flagJSON {
			fmt.Fprintln(stdout, "[]") //nolint:errcheck
			return nil
		}
		fmt.Fprintln(stdout, "No cost data recorded yet. Costs are recorded when sessions end.") //nolint:errcheck
		return nil
	}

	if flagJSON {
		return renderDigestJSON(allRecords, dates, stdout)
	}

	if flagWeek {
		return renderDigestWeekText(allRecords, dates, stdout)
	}
	return renderDigestDayText(allRecords, now, stdout)
}

// aggregateByRole sums input/output tokens per role from a slice of records.
func aggregateByRole(records []costlog.Record) []digestRoleRow {
	type totals struct{ in, out int64 }
	m := make(map[string]*totals)
	for _, r := range records {
		key := r.Role
		if key == "" {
			key = r.Session
		}
		if key == "" {
			key = "(unknown)"
		}
		if m[key] == nil {
			m[key] = &totals{}
		}
		m[key].in += r.InputTokens
		m[key].out += r.OutputTokens
	}
	rows := make([]digestRoleRow, 0, len(m))
	for role, t := range m {
		rows = append(rows, digestRoleRow{Role: role, InputTokens: t.in, OutputTokens: t.out})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].InputTokens != rows[j].InputTokens {
			return rows[i].InputTokens > rows[j].InputTokens
		}
		return rows[i].Role < rows[j].Role
	})
	return rows
}

// aggregateByRig sums input tokens per rig.
func aggregateByRig(records []costlog.Record) []digestRigRow {
	m := make(map[string]int64)
	for _, r := range records {
		key := r.Rig
		if key == "" {
			key = "(hq)"
		}
		m[key] += r.InputTokens
	}
	rows := make([]digestRigRow, 0, len(m))
	for rig, in := range m {
		rows = append(rows, digestRigRow{Rig: rig, InputTokens: in})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].InputTokens != rows[j].InputTokens {
			return rows[i].InputTokens > rows[j].InputTokens
		}
		return rows[i].Rig < rows[j].Rig
	})
	return rows
}

// sumTotals returns total input and output tokens.
func sumTotals(records []costlog.Record) (int64, int64) {
	var in, out int64
	for _, r := range records {
		in += r.InputTokens
		out += r.OutputTokens
	}
	return in, out
}

// renderDigestDayText writes a human-readable single-day digest to w.
func renderDigestDayText(records []costlog.Record, date time.Time, w io.Writer) error {
	fmt.Fprintf(w, "=== Token Usage Digest: %s ===\n\n", date.Format("2006-01-02")) //nolint:errcheck

	byRole := aggregateByRole(records)
	if len(byRole) > 0 {
		fmt.Fprintln(w, "By Role:") //nolint:errcheck
		for _, row := range byRole {
			fmt.Fprintf(w, "  %-24s %s input  (%s output)\n", //nolint:errcheck
				row.Role,
				formatTokens(row.InputTokens),
				formatTokens(row.OutputTokens),
			)
		}
		fmt.Fprintln(w) //nolint:errcheck
	}

	byRig := aggregateByRig(records)
	if len(byRig) > 0 {
		fmt.Fprintln(w, "By Rig:") //nolint:errcheck
		for _, row := range byRig {
			fmt.Fprintf(w, "  %-24s %s input\n", row.Rig, formatTokens(row.InputTokens)) //nolint:errcheck
		}
		fmt.Fprintln(w) //nolint:errcheck
	}

	totalIn, totalOut := sumTotals(records)
	fmt.Fprintf(w, "TOTAL: %s input tokens, %s output tokens\n", //nolint:errcheck
		formatTokens(totalIn), formatTokens(totalOut))
	return nil
}

// renderDigestWeekText writes a human-readable weekly digest to w.
func renderDigestWeekText(records []costlog.Record, dates []time.Time, w io.Writer) error {
	start := dates[0].Format("2006-01-02")
	end := dates[len(dates)-1].Format("2006-01-02")
	fmt.Fprintf(w, "=== Token Usage Digest: %s to %s ===\n\n", start, end) //nolint:errcheck

	// Group records by date.
	byDate := make(map[string][]costlog.Record)
	for _, r := range records {
		d := r.Timestamp.UTC().Format("2006-01-02")
		byDate[d] = append(byDate[d], r)
	}

	for _, date := range dates {
		d := date.Format("2006-01-02")
		recs := byDate[d]
		if len(recs) == 0 {
			continue
		}
		in, out := sumTotals(recs)
		fmt.Fprintf(w, "  %s   %s input, %s output\n", d, formatTokens(in), formatTokens(out)) //nolint:errcheck
	}
	fmt.Fprintln(w) //nolint:errcheck

	byRole := aggregateByRole(records)
	if len(byRole) > 0 {
		fmt.Fprintln(w, "By Role:") //nolint:errcheck
		for _, row := range byRole {
			fmt.Fprintf(w, "  %-24s %s input  (%s output)\n", //nolint:errcheck
				row.Role,
				formatTokens(row.InputTokens),
				formatTokens(row.OutputTokens),
			)
		}
		fmt.Fprintln(w) //nolint:errcheck
	}

	byRig := aggregateByRig(records)
	if len(byRig) > 0 {
		fmt.Fprintln(w, "By Rig:") //nolint:errcheck
		for _, row := range byRig {
			fmt.Fprintf(w, "  %-24s %s input\n", row.Rig, formatTokens(row.InputTokens)) //nolint:errcheck
		}
		fmt.Fprintln(w) //nolint:errcheck
	}

	totalIn, totalOut := sumTotals(records)
	fmt.Fprintf(w, "TOTAL: %s input tokens, %s output tokens\n", //nolint:errcheck
		formatTokens(totalIn), formatTokens(totalOut))
	return nil
}

// renderDigestJSON writes a JSON array of digest summaries grouped by date.
func renderDigestJSON(records []costlog.Record, dates []time.Time, w io.Writer) error {
	// Group by date.
	byDate := make(map[string][]costlog.Record)
	for _, r := range records {
		d := r.Timestamp.UTC().Format("2006-01-02")
		byDate[d] = append(byDate[d], r)
	}

	var days []digestDay
	for _, date := range dates {
		d := date.Format("2006-01-02")
		recs := byDate[d]
		if len(recs) == 0 {
			continue
		}
		in, out := sumTotals(recs)
		days = append(days, digestDay{
			Date:        d,
			ByRole:      aggregateByRole(recs),
			ByRig:       aggregateByRig(recs),
			TotalInput:  in,
			TotalOutput: out,
		})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(days)
}

// formatTokens formats a token count as a comma-separated number string.
func formatTokens(n int64) string {
	s := fmt.Sprintf("%d", n)
	// Insert commas.
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	rem := len(s) % 3
	if rem > 0 {
		b.WriteString(s[:rem])
	}
	for i := rem; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
