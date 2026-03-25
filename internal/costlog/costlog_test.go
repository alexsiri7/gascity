package costlog_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/costlog"
)

func TestAppendRecord(t *testing.T) {
	dir := t.TempDir()
	costsDir := filepath.Join(dir, "costs")

	ts := time.Date(2026, 3, 25, 1, 23, 45, 0, time.UTC)
	rec := costlog.Record{
		Timestamp:     ts,
		Session:       "gascity/polecat-1",
		Rig:           "gascity",
		Role:          "polecat",
		Model:         "claude-sonnet-4-6",
		InputTokens:   123456,
		CacheRead:     45000,
		CacheCreation: 12000,
		OutputTokens:  8900,
	}

	if err := costlog.AppendRecord(costsDir, rec); err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}

	// File should be created.
	logPath := filepath.Join(costsDir, "2026-03-25.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var got costlog.Record
	if err := json.Unmarshal(data[:len(data)-1], &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Session != rec.Session {
		t.Errorf("Session: got %q, want %q", got.Session, rec.Session)
	}
	if got.InputTokens != rec.InputTokens {
		t.Errorf("InputTokens: got %d, want %d", got.InputTokens, rec.InputTokens)
	}
	if got.CacheRead != rec.CacheRead {
		t.Errorf("CacheRead: got %d, want %d", got.CacheRead, rec.CacheRead)
	}
	if got.CacheCreation != rec.CacheCreation {
		t.Errorf("CacheCreation: got %d, want %d", got.CacheCreation, rec.CacheCreation)
	}
	if got.OutputTokens != rec.OutputTokens {
		t.Errorf("OutputTokens: got %d, want %d", got.OutputTokens, rec.OutputTokens)
	}
	if got.Model != rec.Model {
		t.Errorf("Model: got %q, want %q", got.Model, rec.Model)
	}
}

func TestAppendRecord_MultipleRecords(t *testing.T) {
	dir := t.TempDir()
	costsDir := filepath.Join(dir, "costs")

	ts := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	for i := range 3 {
		rec := costlog.Record{
			Timestamp:   ts,
			Session:     "gascity/polecat-1",
			InputTokens: int64(i * 1000),
		}
		if err := costlog.AppendRecord(costsDir, rec); err != nil {
			t.Fatalf("AppendRecord %d: %v", i, err)
		}
	}

	records, err := costlog.ReadDay(costsDir, ts)
	if err != nil {
		t.Fatalf("ReadDay: %v", err)
	}
	if len(records) != 3 {
		t.Errorf("ReadDay: got %d records, want 3", len(records))
	}
}

func TestAppendRecord_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	costsDir := filepath.Join(dir, "nested", "costs")

	rec := costlog.Record{
		Timestamp: time.Now().UTC(),
		Session:   "test/agent",
	}
	if err := costlog.AppendRecord(costsDir, rec); err != nil {
		t.Fatalf("AppendRecord with nested dir: %v", err)
	}
}

func TestReadDay_NoFile(t *testing.T) {
	dir := t.TempDir()
	records, err := costlog.ReadDay(dir, time.Now())
	if err != nil {
		t.Fatalf("ReadDay on missing file: %v", err)
	}
	if records != nil {
		t.Errorf("ReadDay: got %v, want nil", records)
	}
}

func TestReadDay_DateBoundary(t *testing.T) {
	dir := t.TempDir()

	// Write record on day 1 and day 2.
	day1 := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)

	_ = costlog.AppendRecord(dir, costlog.Record{Timestamp: day1, Session: "a/b", InputTokens: 100})
	_ = costlog.AppendRecord(dir, costlog.Record{Timestamp: day2, Session: "a/b", InputTokens: 200})

	records1, _ := costlog.ReadDay(dir, day1)
	records2, _ := costlog.ReadDay(dir, day2)

	if len(records1) != 1 {
		t.Errorf("day1: got %d records, want 1", len(records1))
	}
	if len(records2) != 1 {
		t.Errorf("day2: got %d records, want 1", len(records2))
	}
	if len(records1) > 0 && records1[0].InputTokens != 100 {
		t.Errorf("day1 InputTokens: got %d, want 100", records1[0].InputTokens)
	}
	if len(records2) > 0 && records2[0].InputTokens != 200 {
		t.Errorf("day2 InputTokens: got %d, want 200", records2[0].InputTokens)
	}
}
