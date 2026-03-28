package main

import "github.com/gastownhall/gascity/internal/beads"

// sessionBeadSnapshot caches open session-bead state for a single reconcile
// cycle so build/sync/reconcile can reuse one store scan.
type sessionBeadSnapshot struct {
	open                      []beads.Bead
	sessionNameByAgentName    map[string]string
	sessionNameByTemplateHint map[string]string
	providerByAgentName       map[string]string
}

func loadSessionBeadSnapshot(store beads.Store) (*sessionBeadSnapshot, error) {
	open, err := loadSessionBeads(store)
	if err != nil {
		return nil, err
	}
	return newSessionBeadSnapshot(open), nil
}

func newSessionBeadSnapshot(open []beads.Bead) *sessionBeadSnapshot {
	filtered := make([]beads.Bead, 0, len(open))
	sessionNameByAgentName := make(map[string]string)
	sessionNameByTemplateHint := make(map[string]string)
	providerByAgentName := make(map[string]string)

	for _, b := range open {
		if b.Status == "closed" {
			continue
		}
		filtered = append(filtered, b)

		sn := b.Metadata["session_name"]
		if sn == "" {
			continue
		}
		if agentName := sessionBeadAgentName(b); agentName != "" {
			if _, exists := sessionNameByAgentName[agentName]; !exists {
				sessionNameByAgentName[agentName] = sn
				if prov := b.Metadata["provider"]; prov != "" {
					providerByAgentName[agentName] = prov
				}
			}
		}
		if b.Metadata["pool_slot"] != "" {
			continue
		}
		if template := b.Metadata["template"]; template != "" {
			if _, exists := sessionNameByTemplateHint[template]; !exists {
				sessionNameByTemplateHint[template] = sn
			}
		}
		if commonName := b.Metadata["common_name"]; commonName != "" {
			if _, exists := sessionNameByTemplateHint[commonName]; !exists {
				sessionNameByTemplateHint[commonName] = sn
			}
		}
	}

	return &sessionBeadSnapshot{
		open:                      filtered,
		sessionNameByAgentName:    sessionNameByAgentName,
		sessionNameByTemplateHint: sessionNameByTemplateHint,
		providerByAgentName:       providerByAgentName,
	}
}

func (s *sessionBeadSnapshot) Open() []beads.Bead {
	if s == nil {
		return nil
	}
	result := make([]beads.Bead, len(s.open))
	copy(result, s.open)
	return result
}

func (s *sessionBeadSnapshot) FindSessionNameByTemplate(template string) string {
	if s == nil {
		return ""
	}
	if sn := s.sessionNameByAgentName[template]; sn != "" {
		return sn
	}
	return s.sessionNameByTemplateHint[template]
}

// FindProviderByAgentName returns the provider stored in the session bead for
// the given agent name. Returns empty string if not found or no provider set.
// Used by resolveTemplate to avoid re-selecting a quota-exhausted provider.
func (s *sessionBeadSnapshot) FindProviderByAgentName(agentName string) string {
	if s == nil {
		return ""
	}
	return s.providerByAgentName[agentName]
}
