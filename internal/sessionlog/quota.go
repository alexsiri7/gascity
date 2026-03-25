package sessionlog

import (
	"bytes"
	"os"
)

// quotaPatterns are raw byte strings that appear in session transcripts when
// the provider account has hit its quota limit. The patterns are checked
// against raw JSONL bytes to avoid full parsing and to handle variations in
// how different providers encode error messages.
var quotaPatterns = [][]byte{
	[]byte("You've hit your limit"),
	[]byte("you've hit your limit"),
	[]byte("rate limit exceeded"),
	[]byte("Rate limit exceeded"),
	[]byte("quota exceeded"),
	[]byte("Quota exceeded"),
	[]byte("account has been rate limited"),
}

// DetectQuotaExhaustion reads the tail of a session JSONL file and returns
// true if it contains provider account quota exhaustion patterns. Uses a raw
// byte scan rather than full JSON parsing for robustness across provider
// formats. Returns false for any read error.
func DetectQuotaExhaustion(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck // read-only file

	data, err := readTail(f, tailChunkSize)
	if err != nil {
		return false
	}
	for _, pat := range quotaPatterns {
		if bytes.Contains(data, pat) {
			return true
		}
	}
	return false
}
