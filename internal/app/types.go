package app

import "time"

const (
	ConnectionTransportHTTP      = "http"
	ConnectionTransportWebSocket = "ws"
	ConnectionTransportOffline   = "offline"

	ConnectionStatusDisconnected = "disconnected"
	ConnectionStatusConnected    = "connected"
)

type Settings struct {
	ListenAddr       string        `json:"listen_addr"`
	HubRegion        string        `json:"hub_region"`
	HubURL           string        `json:"hub_url"`
	SessionKey       string        `json:"session_key"`
	PollInterval     time.Duration `json:"poll_interval"`
	TaskTimeout      time.Duration `json:"task_timeout"`
	DataDir          string        `json:"data_dir"`
}

type ConnectionState struct {
	Status        string    `json:"status"`
	Transport     string    `json:"transport"`
	LastChangedAt time.Time `json:"last_changed_at"`
	Error         string    `json:"error,omitempty"`
}

type Session struct {
	BoundAt         time.Time `json:"bound_at"`
	HubURL          string    `json:"hub_url"`
	APIBase         string    `json:"api_base"`
	AgentToken      string    `json:"agent_token"`
	AgentUUID       string    `json:"agent_uuid"`
	AgentURI        string    `json:"agent_uri"`
	Handle          string    `json:"handle"`
	HandleFinalized bool    `json:"handle_finalized"`
	DisplayName     string    `json:"display_name"`
	Emoji           string    `json:"emoji"`
	ProfileBio      string    `json:"profile_bio"`
	ManifestURL     string    `json:"manifest_url"`
	Capabilities    string    `json:"capabilities_url"`
	OfflineMarked   bool      `json:"offline_marked"`
}

type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ConnectedAgent struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	AgentUUID         string    `json:"agent_uuid"`
	AgentURI          string    `json:"agent_uri"`
	DefaultSkill      string    `json:"default_skill"`
	FailureReviewer   bool      `json:"failure_reviewer"`
	Repo              string    `json:"repo"`
	Notes             string    `json:"notes"`
	AdvertisedSkills  []Skill   `json:"advertised_skills"`
	CreatedAt         time.Time `json:"created_at"`
	LastDispatchAt    time.Time `json:"last_dispatch_at"`
	LastDispatchError string    `json:"last_dispatch_error"`
}

type RuntimeEvent struct {
	At      time.Time `json:"at"`
	Level   string    `json:"level"`
	Title   string    `json:"title"`
	Detail  string    `json:"detail"`
	TaskID  string    `json:"task_id"`
	LogPath string    `json:"log_path"`
}

type PendingTask struct {
	ID                string         `json:"id"`
	ParentRequestID   string         `json:"parent_request_id"`
	ChildRequestID    string         `json:"child_request_id"`
	OriginalSkillName string         `json:"original_skill_name"`
	TargetAgentUUID   string         `json:"target_agent_uuid"`
	TargetAgentURI    string         `json:"target_agent_uri"`
	CallerAgentUUID   string         `json:"caller_agent_uuid"`
	CallerAgentURI    string         `json:"caller_agent_uri"`
	CallerRequestID   string         `json:"caller_request_id"`
	Repo              string         `json:"repo"`
	LogPath           string         `json:"log_path"`
	CreatedAt         time.Time      `json:"created_at"`
	ExpiresAt         time.Time      `json:"expires_at"`
	DispatchPayload   map[string]any `json:"dispatch_payload"`
}

type FollowUpRunConfig struct {
	Repos        []string `json:"repos"`
	BaseBranch   string   `json:"baseBranch"`
	TargetSubdir string   `json:"targetSubdir"`
	Prompt       string   `json:"prompt"`
}

type FollowUpTask struct {
	ID               string            `json:"id"`
	CreatedAt        time.Time         `json:"created_at"`
	Status           string            `json:"status"`
	Reason           string            `json:"reason"`
	FailedTaskID     string            `json:"failed_task_id"`
	FailedSkillName  string            `json:"failed_skill_name"`
	FailedRepo       string            `json:"failed_repo"`
	LogPaths         []string          `json:"log_paths"`
	TargetAgentUUID  string            `json:"target_agent_uuid"`
	TargetAgentURI   string            `json:"target_agent_uri"`
	LastDispatchErr  string            `json:"last_dispatch_error"`
	RunConfig        FollowUpRunConfig `json:"run_config"`
	OriginalError    string            `json:"original_error"`
	OriginalRequest  map[string]any    `json:"original_request"`
	RequestedByAgent string            `json:"requested_by_agent"`
}

type AppState struct {
	Settings        Settings         `json:"settings"`
	Session         Session          `json:"session"`
	Connection      ConnectionState  `json:"connection"`
	ConnectedAgents []ConnectedAgent `json:"connected_agents"`
	PendingTasks    []PendingTask    `json:"pending_tasks"`
	FollowUpTasks   []FollowUpTask   `json:"follow_up_tasks"`
	RecentEvents    []RuntimeEvent   `json:"recent_events"`
}

type BindProfile struct {
	BindToken       string
	Handle          string
	DisplayName     string
	Emoji           string
	ProfileMarkdown string
}

type AgentProfile struct {
	Handle          string
	DisplayName     string
	Emoji           string
	ProfileMarkdown string
}

type DispatchRequest struct {
	RequestID      string
	TargetAgentRef string
	SkillName      string
	Repo           string
	LogPaths       []string
	Payload        any
	PayloadFormat  string
	Timeout        time.Duration
}
