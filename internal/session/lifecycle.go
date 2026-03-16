package session

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
)

const (
	// DefaultGeneration is the first runtime epoch for a newly created session.
	DefaultGeneration = 1

	// DefaultContinuationEpoch is the first conversation identity epoch.
	DefaultContinuationEpoch = 1
)

// NewInstanceToken returns a cryptographically random token for fencing
// drain/stop and async delivery against stale session incarnations.
func NewInstanceToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("session: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// RuntimeEnv returns the per-incarnation environment variables a live session
// runtime should receive from the controller/session manager.
func RuntimeEnv(sessionID, sessionName string, generation, continuationEpoch int, instanceToken string) map[string]string {
	env := map[string]string{
		"GC_SESSION_ID":         sessionID,
		"GC_SESSION_NAME":       sessionName,
		"GC_RUNTIME_EPOCH":      strconv.Itoa(generation),
		"GC_CONTINUATION_EPOCH": strconv.Itoa(continuationEpoch),
		"GC_INSTANCE_TOKEN":     instanceToken,
	}
	return env
}
