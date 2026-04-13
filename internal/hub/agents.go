package hub

type AgentPresence struct {
	Status     string `json:"status,omitempty"`
	Ready      *bool  `json:"ready,omitempty"`
	Transport  string `json:"transport,omitempty"`
	SessionKey string `json:"session_key,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

type AgentMetadata struct {
	AgentType       string           `json:"agent_type,omitempty"`
	Public          *bool            `json:"public,omitempty"`
	DisplayName     string           `json:"display_name,omitempty"`
	Emoji           string           `json:"emoji,omitempty"`
	ProfileMarkdown string           `json:"profile_markdown,omitempty"`
	Activities      []any            `json:"activities,omitempty"`
	Skills          []map[string]any `json:"skills,omitempty"`
	HireMe          *bool            `json:"hire_me,omitempty"`
	LLM             string           `json:"llm,omitempty"`
	Harness         string           `json:"harness,omitempty"`
	Presence        *AgentPresence   `json:"presence,omitempty"`
}

type AgentOwner struct {
	HumanID string `json:"human_id,omitempty"`
	OrgID   string `json:"org_id,omitempty"`
}

type HubAgent struct {
	AgentUUID string         `json:"agent_uuid"`
	AgentID   string         `json:"agent_id,omitempty"`
	URI       string         `json:"uri,omitempty"`
	Handle    string         `json:"handle,omitempty"`
	Status    string         `json:"status,omitempty"`
	Metadata  *AgentMetadata `json:"metadata,omitempty"`
	Owner     *AgentOwner    `json:"owner,omitempty"`
}

type ListAgentsResponse struct {
	Agents []HubAgent `json:"agents"`
}
