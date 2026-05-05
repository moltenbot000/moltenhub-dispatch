package app

import (
	"reflect"
	"strings"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

func connectedAgentsFromCapabilities(capabilities map[string]any, state AppState) []ConnectedAgent {
	entries := capabilityPeerCatalogEntries(capabilities)
	connected := make([]ConnectedAgent, 0, len(entries))
	seen := make(map[string]int, len(entries))
	for _, entry := range entries {
		agent := connectedAgentFromCapabilityEntry(entry)
		if sameAgentRef(state.Session, agent.AgentUUID, agent.URI, agent.AgentID, agent.Handle) {
			continue
		}
		key := connectedAgentIdentityKey(agent)
		if key == "" {
			continue
		}
		if index, ok := seen[key]; ok {
			connected[index] = mergeConnectedAgentEntries(connected[index], agent)
			continue
		}
		seen[key] = len(connected)
		connected = append(connected, agent)
	}
	return connected
}

func mergeConnectedAgentEntries(primary, secondary ConnectedAgent) ConnectedAgent {
	merged := primary
	merged.AgentUUID = coalesceTrimmed(merged.AgentUUID, secondary.AgentUUID)
	merged.AgentID = coalesceTrimmed(merged.AgentID, secondary.AgentID)
	merged.URI = coalesceTrimmed(merged.URI, secondary.URI)
	merged.Handle = coalesceTrimmed(merged.Handle, secondary.Handle)
	merged.Status = coalesceTrimmed(secondary.Status, merged.Status)
	merged.DisplayName = coalesceTrimmed(merged.DisplayName, secondary.DisplayName)
	merged.Emoji = coalesceTrimmed(merged.Emoji, secondary.Emoji)
	merged.Presence = mergeConnectedAgentPresence(merged.Presence, secondary.Presence)
	if len(merged.AdvertisedSkills) == 0 {
		merged.AdvertisedSkills = secondary.AdvertisedSkills
	}
	if len(merged.Skills) == 0 {
		merged.Skills = secondary.Skills
	}
	merged.Metadata = mergeConnectedAgentMetadata(merged.Metadata, secondary.Metadata)
	if merged.Owner == nil && secondary.Owner != nil {
		owner := *secondary.Owner
		merged.Owner = &owner
	}
	return normalizeConnectedAgent(merged)
}

func mergeConnectedAgentMetadata(primary, secondary *hub.AgentMetadata) *hub.AgentMetadata {
	switch {
	case primary == nil && secondary == nil:
		return nil
	case primary == nil:
		metadata := *secondary
		return &metadata
	case secondary == nil:
		metadata := *primary
		return &metadata
	}

	metadata := *primary
	metadata.AgentType = coalesceTrimmed(metadata.AgentType, secondary.AgentType)
	metadata.DisplayName = coalesceTrimmed(metadata.DisplayName, secondary.DisplayName)
	metadata.Emoji = coalesceTrimmed(metadata.Emoji, secondary.Emoji)
	metadata.ProfileMarkdown = coalesceTrimmed(metadata.ProfileMarkdown, secondary.ProfileMarkdown)
	metadata.LLM = coalesceTrimmed(metadata.LLM, secondary.LLM)
	metadata.Harness = coalesceTrimmed(metadata.Harness, secondary.Harness)
	if metadata.Public == nil && secondary.Public != nil {
		value := *secondary.Public
		metadata.Public = &value
	}
	if len(metadata.Activities) == 0 {
		metadata.Activities = secondary.Activities
	}
	if len(metadata.AdvertisedSkills) == 0 {
		metadata.AdvertisedSkills = secondary.AdvertisedSkills
	}
	if len(metadata.Skills) == 0 {
		metadata.Skills = secondary.Skills
	}
	if metadata.HireMe == nil && secondary.HireMe != nil {
		value := *secondary.HireMe
		metadata.HireMe = &value
	}
	metadata.Presence = mergeConnectedAgentPresence(metadata.Presence, secondary.Presence)
	if metadataEmpty(&metadata) {
		return nil
	}
	return &metadata
}

func mergeConnectedAgentPresence(primary, secondary *hub.AgentPresence) *hub.AgentPresence {
	switch {
	case primary == nil && secondary == nil:
		return nil
	case primary == nil:
		presence := *secondary
		return &presence
	case secondary == nil:
		presence := *primary
		return &presence
	}

	presence := *primary
	presence.Status = coalesceTrimmed(secondary.Status, presence.Status)
	presence.Transport = coalesceTrimmed(secondary.Transport, presence.Transport)
	presence.SessionKey = coalesceTrimmed(secondary.SessionKey, presence.SessionKey)
	presence.UpdatedAt = coalesceTrimmed(secondary.UpdatedAt, presence.UpdatedAt)
	if secondary.Ready != nil {
		ready := *secondary.Ready
		presence.Ready = &ready
	}
	if presenceEmpty(&presence) {
		return nil
	}
	return &presence
}

type agentIdentity struct {
	AgentUUID       string
	AgentURI        string
	Handle          string
	DisplayName     string
	Emoji           string
	ProfileMarkdown string
}

func existingAgentIdentityFromCapabilities(capabilities map[string]any) agentIdentity {
	roots := []map[string]any{
		capabilities,
		nestedMap(capabilities, "result"),
		nestedMap(capabilities, "self"),
		nestedMap(capabilities, "me"),
		nestedMap(capabilities, "agent"),
	}
	sources := make([]map[string]any, 0, 24)
	seen := make(map[uintptr]struct{}, len(roots))
	for _, root := range roots {
		if len(root) == 0 {
			continue
		}
		ref := reflect.ValueOf(root).Pointer()
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		sources = append(sources, capabilityStringSources(root)...)
	}
	return agentIdentity{
		AgentUUID:       firstCapabilityString(sources, "agent_uuid", "uuid"),
		AgentURI:        firstCapabilityString(sources, "agent_uri", "uri"),
		Handle:          firstCapabilityString(sources, "handle", "agent_id", "id"),
		DisplayName:     firstCapabilityString(sources, "display_name", "displayName", "name"),
		Emoji:           capabilityEmoji(sources),
		ProfileMarkdown: firstCapabilityString(sources, "profile_markdown", "profile", "bio", "description"),
	}
}

func capabilityStringSources(entry map[string]any) []map[string]any {
	sources := make([]map[string]any, 0, 16)
	seen := make(map[uintptr]struct{}, 16)
	appendSource := func(source map[string]any) {
		if len(source) == 0 {
			return
		}
		ref := reflect.ValueOf(source).Pointer()
		if _, ok := seen[ref]; ok {
			return
		}
		seen[ref] = struct{}{}
		sources = append(sources, source)
	}

	entryKeys := []string{
		"metadata",
		"agent",
		"profile",
		"public_profile",
		"directory_profile",
		"directory",
		"public_directory",
		"identity",
		"peer",
		"peer_agent",
		"agent_profile",
	}

	appendSource(entry)
	queue := []map[string]any{entry}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, key := range entryKeys {
			nested := nestedMap(current, key)
			if len(nested) == 0 {
				continue
			}
			ref := reflect.ValueOf(nested).Pointer()
			if _, ok := seen[ref]; ok {
				continue
			}
			appendSource(nested)
			queue = append(queue, nested)
		}
	}

	for i := 0; i < len(sources); i++ {
		if nested := nestedMap(sources[i], "metadata"); len(nested) > 0 {
			appendSource(nested)
		}
	}

	return sources
}

func nestedMap(entry map[string]any, key string) map[string]any {
	value, ok := entry[key]
	if !ok {
		return nil
	}
	mapped, _ := value.(map[string]any)
	return mapped
}

func firstCapabilityString(sources []map[string]any, keys ...string) string {
	for _, key := range keys {
		for _, source := range sources {
			if source == nil {
				continue
			}
			if value := stringFromMap(source, key); value != "" {
				return value
			}
		}
	}
	return ""
}

func capabilityEmoji(sources []map[string]any) string {
	if emoji := firstCapabilityString(sources,
		"emoji",
		"avatar_emoji",
		"display_emoji",
		"profile_emoji",
		"icon_emoji",
		"emoji_native",
		"avatarEmoji",
		"displayEmoji",
		"emojiNative",
		"avatar",
		"icon",
	); emoji != "" {
		return emoji
	}

	for _, source := range sources {
		for _, key := range []string{"avatar", "icon"} {
			nested := nestedMap(source, key)
			if len(nested) == 0 {
				continue
			}
			if emoji := stringFromMap(nested, "emoji", "native", "emoji_native", "emojiNative"); emoji != "" {
				return emoji
			}
		}
	}

	return ""
}

func capabilitySkills(primary map[string]any, metadata map[string]any, agent map[string]any) []Skill {
	for _, source := range []map[string]any{primary, metadata, agent} {
		for _, current := range []map[string]any{source, nestedMap(source, "metadata")} {
			if current == nil {
				continue
			}
			for _, key := range []string{"advertised_skills", "skills"} {
				if skills := skillsFromAny(current[key]); len(skills) > 0 {
					return skills
				}
			}
		}
	}
	return nil
}

func mapsFromAny(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if len(item) > 0 {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			mapped, ok := item.(map[string]any)
			if ok && len(mapped) > 0 {
				out = append(out, mapped)
			}
		}
		return out
	case map[string]any:
		if looksLikeCapabilityPeerEntry(typed) {
			return []map[string]any{typed}
		}
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			mapped, ok := item.(map[string]any)
			if ok && len(mapped) > 0 {
				out = append(out, mapped)
			}
		}
		return out
	default:
		return nil
	}
}

func looksLikeCapabilityPeerEntry(entry map[string]any) bool {
	if len(entry) == 0 {
		return false
	}
	for _, key := range []string{"agent_uuid", "agent_id", "agent_uri", "uri", "handle"} {
		if strings.TrimSpace(stringFromMap(entry, key)) != "" {
			return true
		}
	}
	for _, key := range []string{"agent", "peer", "peer_agent"} {
		if nested := nestedMap(entry, key); len(nested) > 0 {
			return true
		}
	}
	return false
}

func capabilityPeerCatalogEntries(capabilities map[string]any) []map[string]any {
	roots := []map[string]any{
		capabilities,
		nestedMap(capabilities, "result"),
		nestedMap(capabilities, "self"),
		nestedMap(capabilities, "me"),
		nestedMap(capabilities, "agent"),
	}
	entries := make([]map[string]any, 0)
	seen := make(map[uintptr]struct{}, len(roots))
	for _, root := range roots {
		if len(root) == 0 {
			continue
		}
		ref := reflect.ValueOf(root).Pointer()
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		for _, key := range []string{"peer_skill_catalog", "peerSkillCatalog"} {
			entries = append(entries, mapsFromAny(root[key])...)
		}
		peers := capabilityTalkablePeerEntries(root, "control_plane", "controlPlane")
		if len(peers) == 0 {
			peers = capabilityTalkablePeerEntries(root, "communication")
		}
		if len(peers) > 0 {
			entries = append(entries, peers...)
		}
	}
	return entries
}

func capabilityTalkablePeerEntries(root map[string]any, containerKeys ...string) []map[string]any {
	if len(root) == 0 {
		return nil
	}
	for _, containerKey := range containerKeys {
		container := nestedMap(root, containerKey)
		if len(container) == 0 {
			continue
		}
		for _, peersKey := range []string{"talkable_peers", "talkablePeers"} {
			if peers := mapsFromAny(container[peersKey]); len(peers) > 0 {
				return peers
			}
		}
	}
	return nil
}

func connectedAgentFromCapabilityEntry(entry map[string]any) ConnectedAgent {
	sources := capabilityStringSources(entry)
	metadata := connectedAgentMetadataFromCapabilityEntry(entry, sources)
	agentID := firstCapabilityString(sources, "agent_id")
	handle := firstCapabilityString(sources, "handle")
	if agentID == "" {
		agentID = firstCapabilityString(sources, "id")
	}
	if handle == "" {
		handle = agentID
	}
	agent := ConnectedAgent{
		AgentUUID: firstCapabilityString(sources, "agent_uuid", "uuid"),
		AgentID:   agentID,
		URI:       firstCapabilityString(sources, "agent_uri", "uri"),
		Handle:    handle,
		Status:    connectedAgentStatusFromCapabilitySources(sources, metadata),
		Metadata:  metadata,
	}
	return normalizeConnectedAgent(agent)
}

func connectedAgentMetadataFromCapabilityEntry(entry map[string]any, sources []map[string]any) *hub.AgentMetadata {
	metadata := &hub.AgentMetadata{
		AgentType:       firstCapabilityString(sources, "agent_type"),
		DisplayName:     firstCapabilityString(sources, "display_name", "displayName", "name"),
		Emoji:           capabilityEmoji(sources),
		ProfileMarkdown: firstCapabilityString(sources, "profile_markdown", "profile", "bio"),
		LLM:             firstCapabilityString(sources, "llm"),
		Harness:         firstCapabilityString(sources, "harness"),
		Presence:        connectedAgentPresenceFromCapabilitySources(sources),
	}
	if skills := capabilitySkills(entry, nestedMap(entry, "metadata"), nestedMap(entry, "agent")); len(skills) > 0 {
		metadata.AdvertisedSkills = SkillsToMetadata(skills)
	}
	if metadataEmpty(metadata) {
		return nil
	}
	return metadata
}

func connectedAgentPresenceFromCapabilitySources(sources []map[string]any) *hub.AgentPresence {
	for _, source := range sources {
		presence := nestedMap(source, "presence")
		if len(presence) == 0 {
			continue
		}
		status := normalizeCapabilityPresenceStatus(stringFromMap(presence, "status"))
		ready, readyOK := boolFromAny(firstMapValue(presence, "ready"))
		if status == "" && readyOK {
			if ready {
				status = "online"
			} else {
				status = "offline"
			}
		}
		out := &hub.AgentPresence{
			Status:     status,
			Transport:  stringFromMap(presence, "transport"),
			SessionKey: stringFromMap(presence, "session_key", "sessionKey"),
			UpdatedAt:  stringFromMap(presence, "updated_at", "updatedAt"),
		}
		if readyOK {
			out.Ready = &ready
		}
		if !presenceEmpty(out) {
			return out
		}
	}

	status := normalizeCapabilityPresenceStatus(firstCapabilityString(sources, "status"))
	if status == "" {
		return nil
	}
	return &hub.AgentPresence{Status: status}
}

func connectedAgentStatusFromCapabilitySources(sources []map[string]any, metadata *hub.AgentMetadata) string {
	if metadata != nil {
		if status := connectedPresenceStatusFromPresence(metadata.Presence); status != "" {
			return status
		}
	}
	status := normalizeCapabilityPresenceStatus(firstCapabilityString(sources, "status"))
	if status != "" {
		return status
	}
	return ""
}

func normalizeCapabilityPresenceStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "online", "connected", "ready", "available":
		return "online"
	case "offline", "disconnected", "unavailable":
		return "offline"
	default:
		return strings.TrimSpace(status)
	}
}

func SkillsToMetadata(skills []Skill) []map[string]any {
	if len(skills) == 0 {
		return nil
	}
	metadata := make([]map[string]any, 0, len(skills))
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		entry := map[string]any{"name": name}
		if description := strings.TrimSpace(skill.Description); description != "" {
			entry["description"] = description
		}
		metadata = append(metadata, entry)
	}
	return metadata
}

func presenceEmpty(presence *hub.AgentPresence) bool {
	if presence == nil {
		return true
	}
	return strings.TrimSpace(presence.Status) == "" &&
		presence.Ready == nil &&
		strings.TrimSpace(presence.Transport) == "" &&
		strings.TrimSpace(presence.SessionKey) == "" &&
		strings.TrimSpace(presence.UpdatedAt) == ""
}

func metadataEmpty(metadata *hub.AgentMetadata) bool {
	if metadata == nil {
		return true
	}
	return strings.TrimSpace(metadata.AgentType) == "" &&
		strings.TrimSpace(metadata.DisplayName) == "" &&
		strings.TrimSpace(metadata.Emoji) == "" &&
		strings.TrimSpace(metadata.ProfileMarkdown) == "" &&
		strings.TrimSpace(metadata.LLM) == "" &&
		strings.TrimSpace(metadata.Harness) == "" &&
		len(metadata.Activities) == 0 &&
		len(metadata.AdvertisedSkills) == 0 &&
		len(metadata.Skills) == 0 &&
		presenceEmpty(metadata.Presence)
}

func skillsFromAny(value any) []Skill {
	skills := make([]Skill, 0)
	appendSkill := func(item any) {
		switch typed := item.(type) {
		case map[string]any:
			name := strings.TrimSpace(stringFromMap(typed, "name"))
			description := strings.TrimSpace(stringFromMap(typed, "description"))
			if name == "" {
				return
			}
			skills = append(skills, Skill{Name: name, Description: description})
		case Skill:
			name := strings.TrimSpace(typed.Name)
			if name == "" {
				return
			}
			skills = append(skills, Skill{Name: name, Description: strings.TrimSpace(typed.Description)})
		case string:
			name := strings.TrimSpace(typed)
			if name == "" {
				return
			}
			skills = append(skills, Skill{Name: name})
		}
	}

	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			appendSkill(item)
		}
	case []map[string]any:
		for _, item := range typed {
			appendSkill(item)
		}
	case []Skill:
		for _, item := range typed {
			appendSkill(item)
		}
	}

	return skills
}

func sameAgentRef(session Session, refs ...string) bool {
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if strings.EqualFold(ref, session.AgentUUID) || strings.EqualFold(ref, session.AgentURI) || strings.EqualFold(ref, session.Handle) {
			return true
		}
	}
	return false
}
