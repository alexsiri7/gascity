// Package costlog writes per-session token usage records to daily JSONL files.
// Records are appended to {cityRoot}/.gc/runtime/costs/YYYY-MM-DD.jsonl.
// One record is written per session when the session ends (Stop hook).
package costlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Record holds the token usage summary for a single session.
type Record struct {
	Timestamp     time.Time `json:"ts"`
	Session       string    `json:"session"`
	Rig           string    `json:"rig,omitempty"`
	Role          string    `json:"role,omitempty"`
	Model         string    `json:"model,omitempty"`
	InputTokens   int64     `json:"input_tokens"`
	CacheRead     int64     `json:"cache_read"`
	CacheCreation int64     `json:"cache_creation"`
	OutputTokens  int64     `json:"output_tokens"`
}

// AppendRecord appends a cost record to the daily JSONL log file under
// costsDir. The file is named YYYY-MM-DD.jsonl based on the record's Timestamp.
// The directory is created if it does not exist. This function is not
// concurrency-safe — it is designed for single-writer use (one Stop hook per session).
func AppendRecord(costsDir string, rec Record) error {
	if err := os.MkdirAll(costsDir, 0o755); err != nil {
		return fmt.Errorf("creating costs dir %s: %w", costsDir, err)
	}

	date := rec.Timestamp.UTC().Format("2006-01-02")
	path := filepath.Join(costsDir, date+".jsonl")

	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshaling cost record: %w", err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening cost log %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // append-only file

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("writing cost record to %s: %w", path, err)
	}
	return nil
}

// ReadDay reads all cost records from the daily JSONL file for the given date.
// Returns an empty slice if the file does not exist. Malformed lines are skipped.
func ReadDay(costsDir string, date time.Time) ([]Record, error) {
	path := filepath.Join(costsDir, date.UTC().Format("2006-01-02")+".jsonl")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading cost log %s: %w", path, err)
	}

	var records []Record
	dec := json.NewDecoder(nil)
	_ = dec // use manual loop below for line-by-line
	for _, line := range splitLines(data) {
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // skip malformed lines
		}
		records = append(records, rec)
	}
	return records, nil
}

// splitLines splits JSONL data into non-empty lines.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			line := data[start:i]
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(data) {
		line := data[start:]
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}
