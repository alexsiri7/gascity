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

// makeCityDir creates a temp city dir with .gc/ so resolveCity() accepts it.
// Sets GC_CITY env var and returns the city dir and costs dir paths.
func makeCityDir(t *testing.T) (cityDir, costsDir string) {
	t.Helper()
	cityDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatalf("creating .gc: %v", err)
	}
	t.Setenv("GC_CITY", cityDir)
	costsDir = filepath.Join(cityDir, ".gc", "runtime", "costs")
	return cityDir, costsDir
}

// writeCostRecords writes cost records to the costs dir for testing.
func writeCostRecords(t *testing.T, costsDir string, records []costlog.Record) {
	t.Helper()
	for _, rec := range records {
		if err := costlog.AppendRecord(costsDir, rec); err != nil {
			t.Fatalf("AppendRecord: %v", err)
		}
	}
}

func TestFormatTokens(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{1234, "1,234"},
		{123456, "123,456"},
		{1234567, "1,234,567"},
		{402456, "402,456"},
	}
	for _, tc := range cases {
		got := formatTokens(tc.n)
		if got != tc.want {
			t.Errorf("formatTokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestAggregateByRole(t *testing.T) {
	ts := time.Now().UTC()
	records := []costlog.Record{
		{Timestamp: ts, Session: "gascity/polecat-1", Rig: "gascity", Role: "polecat", InputTokens: 100, OutputTokens: 10},
		{Timestamp: ts, Session: "gascity/polecat-2", Rig: "gascity", Role: "polecat", InputTokens: 200, OutputTokens: 20},
		{Timestamp: ts, Session: "mayor", Rig: "", Role: "mayor", InputTokens: 50, OutputTokens: 5},
	}
	rows := aggregateByRole(records)
	// Should have 2 roles: polecat (300 input) and mayor (50 input).
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Role != "polecat" || rows[0].InputTokens != 300 {
		t.Errorf("row[0]: got role=%q input=%d, want polecat/300", rows[0].Role, rows[0].InputTokens)
	}
	if rows[1].Role != "mayor" || rows[1].InputTokens != 50 {
		t.Errorf("row[1]: got role=%q input=%d, want mayor/50", rows[1].Role, rows[1].InputTokens)
	}
}

func TestAggregateByRig(t *testing.T) {
	ts := time.Now().UTC()
	records := []costlog.Record{
		{Timestamp: ts, Rig: "gascity", InputTokens: 100},
		{Timestamp: ts, Rig: "gascity", InputTokens: 200},
		{Timestamp: ts, Rig: "", InputTokens: 50},
	}
	rows := aggregateByRig(records)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].Rig != "gascity" || rows[0].InputTokens != 300 {
		t.Errorf("row[0]: got rig=%q input=%d, want gascity/300", rows[0].Rig, rows[0].InputTokens)
	}
	if rows[1].Rig != "(hq)" || rows[1].InputTokens != 50 {
		t.Errorf("row[1]: got rig=%q input=%d, want (hq)/50", rows[1].Rig, rows[1].InputTokens)
	}
}

func TestCmdCostsDigest_NoData(t *testing.T) {
	_, _ = makeCityDir(t)

	var stdout, stderr bytes.Buffer
	err := cmdCostsDigest(false, false, false, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), "No cost data recorded yet") {
		t.Errorf("expected no-data message, got: %q", stdout.String())
	}
}

func TestCmdCostsDigest_NoDataJSON(t *testing.T) {
	_, _ = makeCityDir(t)

	var stdout, stderr bytes.Buffer
	err := cmdCostsDigest(false, false, true, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != "[]" {
		t.Errorf("expected '[]', got: %q", stdout.String())
	}
}

func TestCmdCostsDigest_Today(t *testing.T) {
	_, costsDir := makeCityDir(t)

	now := time.Now().UTC()
	records := []costlog.Record{
		{
			Timestamp:    now,
			Session:      "gascity/polecat-1",
			Rig:          "gascity",
			Role:         "polecat",
			Model:        "claude-sonnet-4-6",
			InputTokens:  180456,
			OutputTokens: 9200,
		},
		{
			Timestamp:    now,
			Session:      "mayor",
			Rig:          "",
			Role:         "mayor",
			Model:        "claude-sonnet-4-6",
			InputTokens:  124000,
			OutputTokens: 5800,
		},
	}
	writeCostRecords(t, costsDir, records)

	var stdout, stderr bytes.Buffer
	err := cmdCostsDigest(true, false, false, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Token Usage Digest") {
		t.Errorf("missing header, got: %q", out)
	}
	if !strings.Contains(out, "polecat") {
		t.Errorf("missing polecat role, got: %q", out)
	}
	if !strings.Contains(out, "mayor") {
		t.Errorf("missing mayor role, got: %q", out)
	}
	if !strings.Contains(out, "TOTAL") {
		t.Errorf("missing TOTAL line, got: %q", out)
	}
	// Check total input tokens = 180456 + 124000 = 304456
	if !strings.Contains(out, "304,456") {
		t.Errorf("missing total 304,456 in output: %q", out)
	}
}

func TestCmdCostsDigest_TodayJSON(t *testing.T) {
	_, costsDir := makeCityDir(t)

	now := time.Now().UTC()
	records := []costlog.Record{
		{
			Timestamp:    now,
			Session:      "gascity/polecat-1",
			Rig:          "gascity",
			Role:         "polecat",
			InputTokens:  100000,
			OutputTokens: 5000,
		},
	}
	writeCostRecords(t, costsDir, records)

	var stdout, stderr bytes.Buffer
	err := cmdCostsDigest(true, false, true, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var days []digestDay
	if err := json.Unmarshal(stdout.Bytes(), &days); err != nil {
		t.Fatalf("invalid JSON output: %v\nraw: %q", err, stdout.String())
	}
	if len(days) != 1 {
		t.Fatalf("got %d days, want 1", len(days))
	}
	if days[0].TotalInput != 100000 {
		t.Errorf("TotalInput = %d, want 100000", days[0].TotalInput)
	}
	if days[0].TotalOutput != 5000 {
		t.Errorf("TotalOutput = %d, want 5000", days[0].TotalOutput)
	}
	if days[0].Date != now.Format("2006-01-02") {
		t.Errorf("Date = %q, want %q", days[0].Date, now.Format("2006-01-02"))
	}
}

func TestCmdCostsDigest_Week(t *testing.T) {
	_, costsDir := makeCityDir(t)

	now := time.Now().UTC()
	// Write records for today and 3 days ago.
	records := []costlog.Record{
		{
			Timestamp:    now,
			Session:      "gascity/polecat-1",
			Rig:          "gascity",
			Role:         "polecat",
			InputTokens:  50000,
			OutputTokens: 2500,
		},
		{
			Timestamp:    now.AddDate(0, 0, -3),
			Session:      "gascity/polecat-2",
			Rig:          "gascity",
			Role:         "polecat",
			InputTokens:  30000,
			OutputTokens: 1500,
		},
	}
	writeCostRecords(t, costsDir, records)

	var stdout, stderr bytes.Buffer
	err := cmdCostsDigest(false, true, false, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Token Usage Digest") {
		t.Errorf("missing header in weekly output: %q", out)
	}
	// Total = 80000 input
	if !strings.Contains(out, "80,000") {
		t.Errorf("missing total 80,000 in output: %q", out)
	}
}

func TestCmdCostsDigest_WeekJSON(t *testing.T) {
	_, costsDir := makeCityDir(t)

	now := time.Now().UTC()
	records := []costlog.Record{
		{
			Timestamp:    now,
			Session:      "gascity/polecat-1",
			Rig:          "gascity",
			Role:         "polecat",
			InputTokens:  50000,
			OutputTokens: 2500,
		},
		{
			Timestamp:    now.AddDate(0, 0, -1),
			Session:      "mayor",
			Rig:          "",
			Role:         "mayor",
			InputTokens:  20000,
			OutputTokens: 1000,
		},
	}
	writeCostRecords(t, costsDir, records)

	var stdout, stderr bytes.Buffer
	err := cmdCostsDigest(false, true, true, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var days []digestDay
	if err := json.Unmarshal(stdout.Bytes(), &days); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %q", err, stdout.String())
	}
	if len(days) != 2 {
		t.Fatalf("got %d days, want 2", len(days))
	}
}

func TestCmdCostsDigest_RoleWithoutRig(t *testing.T) {
	_, costsDir := makeCityDir(t)

	now := time.Now().UTC()
	records := []costlog.Record{
		{
			Timestamp:    now,
			Session:      "solo-agent",
			Rig:          "",
			Role:         "",
			InputTokens:  10000,
			OutputTokens: 500,
		},
	}
	writeCostRecords(t, costsDir, records)

	var stdout, stderr bytes.Buffer
	err := cmdCostsDigest(true, false, false, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := stdout.String()
	// Session name is used as role key when role is empty.
	if !strings.Contains(out, "solo-agent") {
		t.Errorf("missing solo-agent session name: %q", out)
	}
	// Rig is "(hq)" when empty.
	if !strings.Contains(out, "(hq)") {
		t.Errorf("missing (hq) rig label: %q", out)
	}
}

func TestCmdCostsDigest_NoDataIsolated(t *testing.T) {
	// Verify that with a fresh city that has no cost records, the command
	// produces the "no data" message and does not panic.
	_, _ = makeCityDir(t)

	var stdout, stderr bytes.Buffer
	err := cmdCostsDigest(false, false, false, &stdout, &stderr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "No cost data recorded yet") {
		t.Errorf("expected no-data message, got: %q", out)
	}
}
