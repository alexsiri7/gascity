package sessionlog

import (
	"bufio"
	"encoding/json"
	"os"
)

// TokenTotals holds cumulative token usage summed across all API calls in a session.
type TokenTotals struct {
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	Model               string `json:"model"`
}

// tokenUsageEntry is the minimal structure decoded from each JSONL line
// for token summation.
type tokenUsageEntry struct {
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message"`
}

// tokenUsageMessage extracts usage and model from an assistant message.
type tokenUsageMessage struct {
	Model string `json:"model"`
	Usage *struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// ExtractTokenTotals reads a session JSONL file and sums all token usage
// across every assistant message. Returns nil if the file has no usage data.
func ExtractTokenTotals(path string) (*TokenTotals, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // best-effort close on read-only file

	var totals TokenTotals
	var found bool

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry tokenUsageEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Type != "assistant" || len(entry.Message) == 0 {
			continue
		}

		raw := unwrapJSONString(entry.Message)
		var msg tokenUsageMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		if msg.Model != "" {
			totals.Model = msg.Model
		}
		if msg.Usage != nil {
			totals.InputTokens += msg.Usage.InputTokens
			totals.OutputTokens += msg.Usage.OutputTokens
			totals.CacheReadTokens += msg.Usage.CacheReadInputTokens
			totals.CacheCreationTokens += msg.Usage.CacheCreationInputTokens
			found = true
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &totals, nil
}

// ExtractAllTokenTotals extracts token totals from a main session file
// and all its subagent files, returning a slice of totals (one per file
// with usage data). The main session total is first if present.
func ExtractAllTokenTotals(sessionFile string) ([]*TokenTotals, error) {
	var results []*TokenTotals

	// Main session.
	if main, err := ExtractTokenTotals(sessionFile); err != nil {
		return nil, err
	} else if main != nil {
		results = append(results, main)
	}

	// Subagent files live in {sessionFile-without-ext}/subagents/*.jsonl
	agentFiles, _ := FindAgentFiles(sessionFile)
	for _, af := range agentFiles {
		if sub, err := ExtractTokenTotals(af); err == nil && sub != nil {
			results = append(results, sub)
		}
	}

	return results, nil
}
