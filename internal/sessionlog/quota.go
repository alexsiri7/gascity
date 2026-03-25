package sessionlog

import (
	"encoding/json"
	"os"
	"strings"
)

// QuotaPatterns lists string patterns that indicate a provider account is
// quota-exhausted. Exported so callers can extend or inspect the list.
var QuotaPatterns = []string{
	"You've hit your limit",
	"you've hit your limit",
	"exceeded your current quota",
	"insufficient_quota",
	"Rate limit exceeded",
	"rate limit exceeded",
}

// ScanForQuotaExhausted reads the tail of a JSONL session file and returns
// true if the last tailConsecutive assistant or result entries all contain
// a quota-exhaustion pattern. Returns false on any read or parse error
// (fail-safe: never false-positive).
//
// tailConsecutive <= 0 defaults to 3.
func ScanForQuotaExhausted(path string, tailConsecutive int) bool {
	if tailConsecutive <= 0 {
		tailConsecutive = 3
	}

	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck // best-effort close on read-only file

	data, err := readTail(f, tailChunkSize)
	if err != nil {
		return false
	}

	lines := splitLines(data)

	// Walk backwards collecting the last tailConsecutive assistant/result entries.
	count := 0
	quotaCount := 0
	for i := len(lines) - 1; i >= 0 && count < tailConsecutive; i-- {
		var entry tailEntry
		if err := json.Unmarshal(lines[i], &entry); err != nil {
			continue
		}
		if entry.Type != "assistant" && entry.Type != "result" {
			continue
		}
		count++
		if containsQuotaPattern(string(entry.Message)) {
			quotaCount++
		}
	}

	return count >= tailConsecutive && quotaCount >= tailConsecutive
}

// containsQuotaPattern returns true if s contains any quota exhaustion pattern.
func containsQuotaPattern(s string) bool {
	for _, p := range QuotaPatterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
