package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

func (s *Service) AddConnectedAgent(agent ConnectedAgent) error {
	agent = normalizeConnectedAgent(agent)
	if connectedAgentIdentityKey(agent) == "" {
		return errors.New("connected agent requires agent_id, handle, uri, or agent_uuid")
	}
	return s.store.Update(func(state *AppState) error {
		state.ConnectedAgents = AddOrReplaceConnectedAgent(state.ConnectedAgents, agent)
		return nil
	})
}

func (s *Service) RefreshConnectedAgents(ctx context.Context) ([]ConnectedAgent, error) {
	state := s.store.Snapshot()
	if strings.TrimSpace(state.Session.AgentToken) == "" {
		return state.ConnectedAgents, nil
	}
	s.syncHubClient(state)

	capabilities, err := s.hub.GetCapabilities(ctx, state.Session.AgentToken)
	if err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return nil, fmt.Errorf("refresh connected agents from /v1/agents/me/capabilities: %w", err)
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTP)

	agents := connectedAgentsFromCapabilities(capabilities, state)
	if err := s.store.Update(func(current *AppState) error {
		current.ConnectedAgents = agents
		return nil
	}); err != nil {
		return nil, err
	}
	return agents, nil
}

func connectedAgentNameOrRef(agent ConnectedAgent) string {
	return coalesceTrimmed(
		ConnectedAgentDisplayName(agent),
		connectedAgentSecondaryRef(agent),
	)
}

func ConnectedAgentLabelCandidates(agent ConnectedAgent) []string {
	metadataDisplayName := ""
	if metadata := connectedAgentMetadata(agent); metadata != nil {
		metadataDisplayName = metadata.DisplayName
	}
	return []string{
		metadataDisplayName,
		agent.DisplayName,
		agent.Handle,
		agent.AgentID,
		agent.URI,
		agent.AgentUUID,
	}
}

func ConnectedAgentDisplayName(agent ConnectedAgent) string {
	return coalesceTrimmed(append(ConnectedAgentLabelCandidates(agent), "Unknown agent")...)
}

func connectedAgentSecondaryRef(agent ConnectedAgent) string {
	return coalesceTrimmed(agent.AgentID, agent.URI, agent.Handle, agent.AgentUUID)
}

func ConnectedAgentEmoji(agent ConnectedAgent) string {
	metadata := connectedAgentMetadata(agent)
	if metadata != nil && strings.TrimSpace(metadata.Emoji) != "" {
		return strings.TrimSpace(metadata.Emoji)
	}
	return strings.TrimSpace(agent.Emoji)
}

func ConnectedAgentPresenceStatus(agent ConnectedAgent) string {
	metadata := connectedAgentMetadata(agent)
	if metadata != nil {
		if status := connectedPresenceStatusFromPresence(metadata.Presence); status != "" {
			return status
		}
	}
	if status := connectedPresenceStatusFromPresence(agent.Presence); status != "" {
		return status
	}
	if status := normalizedConnectedPresenceStatus(agent.Status); status != "" {
		return status
	}
	return "offline"
}

func connectedPresenceStatusFromPresence(presence *hub.AgentPresence) string {
	if presence == nil {
		return ""
	}
	if status := normalizedConnectedPresenceStatus(presence.Status); status != "" {
		return status
	}
	if presence.Ready != nil {
		if *presence.Ready {
			return "online"
		}
		return "offline"
	}
	return ""
}

func normalizedConnectedPresenceStatus(status string) string {
	switch normalizeCapabilityPresenceStatus(status) {
	case "online":
		return "online"
	case "offline":
		return "offline"
	default:
		return ""
	}
}

func connectedAgentSupportsSkill(agent ConnectedAgent, skillName string) bool {
	skillName = strings.TrimSpace(skillName)
	if skillName == "" {
		return false
	}
	for _, skill := range ConnectedAgentSkills(agent) {
		if strings.EqualFold(skill.Name, skillName) {
			return true
		}
	}
	return false
}

func ConnectedAgentSkills(agent ConnectedAgent) []Skill {
	metadata := connectedAgentMetadata(agent)
	if metadata != nil {
		for _, raw := range []any{metadata.Skills, metadata.AdvertisedSkills} {
			if skills := skillsFromAny(raw); len(skills) > 0 {
				return skills
			}
		}
	}
	for _, raw := range []any{agent.Skills, agent.AdvertisedSkills} {
		if skills := skillsFromAny(raw); len(skills) > 0 {
			return skills
		}
	}
	return nil
}

func connectedAgentRefs(agent ConnectedAgent) []string {
	refs := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, value := range []string{agent.AgentID, agent.Handle, agent.AgentUUID, agent.URI} {
		ref := strings.TrimSpace(value)
		if ref == "" {
			continue
		}
		key := strings.ToLower(ref)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}

func connectedAgentIdentityKey(agent ConnectedAgent) string {
	return strings.ToLower(coalesceTrimmed(agent.AgentUUID, agent.URI, agent.AgentID, agent.Handle))
}

func connectedAgentMetadata(agent ConnectedAgent) *hub.AgentMetadata {
	return agent.Metadata
}

func normalizeConnectedAgent(agent ConnectedAgent) ConnectedAgent {
	agent.AgentUUID = strings.TrimSpace(agent.AgentUUID)
	agent.AgentID = strings.TrimSpace(agent.AgentID)
	agent.URI = strings.TrimSpace(agent.URI)
	agent.Handle = strings.TrimSpace(agent.Handle)
	agent.Status = strings.TrimSpace(agent.Status)
	agent.DisplayName = strings.TrimSpace(agent.DisplayName)
	agent.Emoji = strings.TrimSpace(agent.Emoji)
	if agent.Presence != nil {
		presence := *agent.Presence
		presence.Status = strings.TrimSpace(presence.Status)
		presence.Transport = strings.TrimSpace(presence.Transport)
		presence.SessionKey = strings.TrimSpace(presence.SessionKey)
		presence.UpdatedAt = strings.TrimSpace(presence.UpdatedAt)
		agent.Presence = &presence
	}
	if agent.Metadata != nil {
		metadata := *agent.Metadata
		metadata.AgentType = strings.TrimSpace(metadata.AgentType)
		metadata.DisplayName = strings.TrimSpace(metadata.DisplayName)
		metadata.Emoji = strings.TrimSpace(metadata.Emoji)
		metadata.ProfileMarkdown = strings.TrimSpace(metadata.ProfileMarkdown)
		metadata.LLM = strings.TrimSpace(metadata.LLM)
		metadata.Harness = strings.TrimSpace(metadata.Harness)
		if metadata.Presence != nil {
			presence := *metadata.Presence
			presence.Status = strings.TrimSpace(presence.Status)
			presence.Transport = strings.TrimSpace(presence.Transport)
			presence.SessionKey = strings.TrimSpace(presence.SessionKey)
			presence.UpdatedAt = strings.TrimSpace(presence.UpdatedAt)
			metadata.Presence = &presence
		}
		if agent.Metadata.Skills == nil {
			metadata.Skills = nil
		}
		if agent.Metadata.AdvertisedSkills == nil {
			metadata.AdvertisedSkills = nil
		}
		if agent.Metadata.Activities == nil {
			metadata.Activities = nil
		}
		agent.Metadata = &metadata
	}
	if agent.Skills == nil {
		agent.Skills = nil
	}
	if agent.AdvertisedSkills == nil {
		agent.AdvertisedSkills = nil
	}
	return agent
}
