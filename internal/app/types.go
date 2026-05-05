package app

import (
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

const (
	ConnectionTransportHTTP      = "http"
	ConnectionTransportConnected = "connected"
	ConnectionTransportReachable = "reachable"
	ConnectionTransportRetrying  = "retrying"
	ConnectionTransportHTTPLong  = "http_long_poll"
	ConnectionTransportWebSocket = "ws"
	ConnectionTransportOffline   = "offline"

	ConnectionStatusDisconnected = "disconnected"
	ConnectionStatusConnected    = "connected"

	PendingTaskStatusSending = "sending"
	PendingTaskStatusInQueue = "in_queue"

	ScheduledMessageStatusActive = "active"

	DispatchSelectionRequiredMessage = "Please select agent, skill to dispatch a request."
)

type Settings struct {
	ListenAddr                   string        `json:"listen_addr"`
	HubRegion                    string        `json:"hub_region"`
	HubURL                       string        `json:"hub_url"`
	SessionKey                   string        `json:"session_key"`
	PollInterval                 time.Duration `json:"poll_interval"`
	TaskTimeout                  time.Duration `json:"task_timeout"`
	DataDir                      string        `json:"data_dir"`
	GoogleAnalyticsMeasurementID string        `json:"google_analytics_measurement_id,omitempty"`
}

type ConnectionState struct {
	Status        string    `json:"status"`
	Transport     string    `json:"transport"`
	LastChangedAt time.Time `json:"last_changed_at"`
	Error         string    `json:"error,omitempty"`
	Detail        string    `json:"detail,omitempty"`
	BaseURL       string    `json:"base_url,omitempty"`
	Domain        string    `json:"domain,omitempty"`
}

type Session struct {
	BoundAt         time.Time `json:"bound_at"`
	HubURL          string    `json:"hub_url"`
	APIBase         string    `json:"api_base"`
	AgentToken      string    `json:"agent_token"`
	BaseURL         string    `json:"base_url,omitempty"`
	BindToken       string    `json:"bind_token,omitempty"`
	AgentUUID       string    `json:"agent_uuid"`
	AgentURI        string    `json:"agent_uri"`
	Handle          string    `json:"handle"`
	HandleFinalized bool      `json:"handle_finalized"`
	DisplayName     string    `json:"display_name"`
	Emoji           string    `json:"emoji"`
	ProfileBio      string    `json:"profile_bio"`
	ManifestURL     string    `json:"manifest_url"`
	MetadataURL     string    `json:"metadata_url"`
	Capabilities    string    `json:"capabilities_url"`
	OpenClawPullURL string    `json:"openclaw_pull_url"`
	OpenClawPushURL string    `json:"openclaw_push_url"`
	OfflineURL      string    `json:"offline_url"`
	OfflineMarked   bool      `json:"offline_marked"`
}

type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type ConnectedAgent = hub.HubAgent

type RuntimeEvent struct {
	At                     time.Time `json:"at"`
	Level                  string    `json:"level"`
	Title                  string    `json:"title"`
	Detail                 string    `json:"detail"`
	TaskID                 string    `json:"task_id"`
	HubTaskID              string    `json:"hub_task_id,omitempty"`
	ChildRequestID         string    `json:"child_request_id,omitempty"`
	LogPath                string    `json:"log_path"`
	OriginalSkillName      string    `json:"original_skill_name"`
	TargetAgentDisplayName string    `json:"target_agent_display_name"`
	TargetAgentEmoji       string    `json:"target_agent_emoji"`
	TargetAgentUUID        string    `json:"target_agent_uuid"`
	TargetAgentURI         string    `json:"target_agent_uri"`
}

type FlashMessage struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

type PendingTask struct {
	ID                     string         `json:"id"`
	Status                 string         `json:"status"`
	HubTaskID              string         `json:"hub_task_id,omitempty"`
	ParentRequestID        string         `json:"parent_request_id"`
	ChildRequestID         string         `json:"child_request_id"`
	OriginalSkillName      string         `json:"original_skill_name"`
	TargetAgentDisplayName string         `json:"target_agent_display_name"`
	TargetAgentEmoji       string         `json:"target_agent_emoji"`
	TargetAgentUUID        string         `json:"target_agent_uuid"`
	TargetAgentURI         string         `json:"target_agent_uri"`
	CallerAgentUUID        string         `json:"caller_agent_uuid"`
	CallerAgentURI         string         `json:"caller_agent_uri"`
	CallerRequestID        string         `json:"caller_request_id"`
	Repo                   string         `json:"repo"`
	LogPath                string         `json:"log_path"`
	CreatedAt              time.Time      `json:"created_at"`
	ExpiresAt              time.Time      `json:"expires_at"`
	DispatchPayload        map[string]any `json:"dispatch_payload"`
	DispatchPayloadFormat  string         `json:"dispatch_payload_format"`
	ExecutionRetryCount    int            `json:"execution_retry_count"`
	PreferA2A              bool           `json:"prefer_a2a,omitempty"`
	DownstreamStatus       string         `json:"downstream_status,omitempty"`
	DownstreamTaskState    string         `json:"downstream_task_state,omitempty"`
	DownstreamMessage      string         `json:"downstream_message,omitempty"`
	DownstreamUpdatedAt    time.Time      `json:"downstream_updated_at,omitempty"`
}

type ScheduledMessage struct {
	ID                     string         `json:"id"`
	Status                 string         `json:"status"`
	ParentRequestID        string         `json:"parent_request_id"`
	OriginalSkillName      string         `json:"original_skill_name"`
	TargetAgentRef         string         `json:"target_agent_ref"`
	TargetAgentDisplayName string         `json:"target_agent_display_name"`
	TargetAgentEmoji       string         `json:"target_agent_emoji"`
	TargetAgentUUID        string         `json:"target_agent_uuid"`
	TargetAgentURI         string         `json:"target_agent_uri"`
	CallerAgentUUID        string         `json:"caller_agent_uuid"`
	CallerAgentURI         string         `json:"caller_agent_uri"`
	CallerRequestID        string         `json:"caller_request_id"`
	Repo                   string         `json:"repo"`
	LogPaths               []string       `json:"log_paths"`
	CreatedAt              time.Time      `json:"created_at"`
	NextRunAt              time.Time      `json:"next_run_at"`
	LastRunAt              time.Time      `json:"last_run_at,omitempty"`
	Frequency              time.Duration  `json:"frequency,omitempty"`
	Cron                   string         `json:"cron,omitempty"`
	DispatchPayload        map[string]any `json:"dispatch_payload"`
	DispatchPayloadFormat  string         `json:"dispatch_payload_format"`
	Timeout                time.Duration  `json:"timeout"`
	PreferA2A              bool           `json:"prefer_a2a,omitempty"`
}

type AppState struct {
	Settings          Settings           `json:"settings"`
	Session           Session            `json:"session"`
	Connection        ConnectionState    `json:"connection"`
	Flash             FlashMessage       `json:"flash"`
	ConnectedAgents   []ConnectedAgent   `json:"connected_agents"`
	PendingTasks      []PendingTask      `json:"pending_tasks"`
	ScheduledMessages []ScheduledMessage `json:"scheduled_messages"`
	RecentEvents      []RuntimeEvent     `json:"recent_events"`
}

type BindProfile struct {
	AgentMode       string
	AgentToken      string
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
	ScheduledAt    time.Time
	Frequency      time.Duration
	PreferA2A      bool
}
