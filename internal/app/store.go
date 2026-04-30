package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxRecentEvents = 40

const (
	defaultDataDir                  = ".moltenhub"
	defaultGoogleAnalyticsMeasureID = "G-BY33RFG2WB"
	moltenHubRegionEnvVar           = "MOLTEN_HUB_REGION"
)

type Store struct {
	path  string
	mu    sync.RWMutex
	state AppState
}

type persistedConfig struct {
	Settings          persistedSettings  `json:"settings,omitempty"`
	Session           persistedSession   `json:"session,omitempty"`
	ScheduledMessages []ScheduledMessage `json:"scheduled_messages,omitempty"`
}

type persistedSettings struct {
	HubURL    string `json:"hub_url,omitempty"`
	HubRegion string `json:"hub_region,omitempty"`
}

type persistedSession struct {
	AgentToken string `json:"agent_token,omitempty"`
	BindToken  string `json:"bind_token,omitempty"`
}

func DefaultSettings() Settings {
	runtime := defaultRuntimeFromEnv()
	return Settings{
		ListenAddr:                   envOrDefault("LISTEN_ADDR", ":8080"),
		HubRegion:                    runtime.ID,
		HubURL:                       runtime.HubURL,
		SessionKey:                   "main",
		PollInterval:                 2 * time.Second,
		TaskTimeout:                  5 * time.Minute,
		DataDir:                      envOrDefault("APP_DATA_DIR", defaultDataDir),
		GoogleAnalyticsMeasurementID: envOrDefault("MOLTENHUB_GOOGLE_ANALYTICS_ID", defaultGoogleAnalyticsMeasureID),
	}
}

func ResolveStorePath(dataDir string) (string, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("create data directory: %w", err)
	}

	configPath := filepath.Join(dataDir, "config.json")
	legacyPath := filepath.Join(dataDir, "state.json")

	if _, err := os.Stat(configPath); err == nil {
		return configPath, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat config store: %w", err)
	}

	if _, err := os.Stat(legacyPath); err == nil {
		if err := os.Rename(legacyPath, configPath); err != nil {
			return "", fmt.Errorf("migrate legacy state store: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat legacy state store: %w", err)
	}

	return configPath, nil
}

func NewStore(path string, defaults Settings) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}

	store := &Store{
		path: path,
		state: AppState{
			Settings: defaults,
			Connection: ConnectionState{
				Status:    ConnectionStatusDisconnected,
				Transport: ConnectionTransportOffline,
			},
		},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := store.saveLocked(); err != nil {
				return nil, err
			}
			return store, nil
		}
		return nil, fmt.Errorf("read store: %w", err)
	}

	if len(data) == 0 {
		return store, nil
	}

	var persisted persistedConfig
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, fmt.Errorf("decode store: %w", err)
	}
	applyPersistedConfig(&store.state, persisted)

	mergeDefaultSettings(&store.state.Settings, defaults)
	normalizeStateAliases(&store.state)
	if strings.TrimSpace(store.state.Connection.Status) == "" {
		store.state.Connection.Status = ConnectionStatusDisconnected
	}
	if strings.TrimSpace(store.state.Connection.Transport) == "" {
		store.state.Connection.Transport = ConnectionTransportOffline
	}
	return store, nil
}

func (s *Store) Snapshot() AppState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneState(s.state)
}

func (s *Store) Update(fn func(*AppState) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := fn(&s.state); err != nil {
		return err
	}
	normalizeStateAliases(&s.state)
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(persistedConfigFromState(s.state), "", "  ")
	if err != nil {
		return fmt.Errorf("encode store: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		return fmt.Errorf("write store: %w", err)
	}
	return nil
}

func (s *Store) AppendEvent(event RuntimeEvent) error {
	return s.Update(func(state *AppState) error {
		state.RecentEvents = append([]RuntimeEvent{event}, state.RecentEvents...)
		if len(state.RecentEvents) > maxRecentEvents {
			state.RecentEvents = state.RecentEvents[:maxRecentEvents]
		}
		return nil
	})
}

func FindConnectedAgent(agents []ConnectedAgent, ref string) (ConnectedAgent, bool) {
	ref = strings.TrimSpace(ref)
	for _, agent := range agents {
		if ref == "" {
			continue
		}
		if strings.EqualFold(agent.AgentUUID, ref) ||
			strings.EqualFold(agent.AgentID, ref) ||
			strings.EqualFold(agent.Handle, ref) ||
			strings.EqualFold(agent.URI, ref) ||
			strings.EqualFold(ConnectedAgentDisplayName(agent), ref) {
			return agent, true
		}
	}
	return ConnectedAgent{}, false
}

func AddOrReplaceConnectedAgent(agents []ConnectedAgent, next ConnectedAgent) []ConnectedAgent {
	nextKey := connectedAgentIdentityKey(next)
	if nextKey == "" {
		return agents
	}
	for i, existing := range agents {
		if connectedAgentIdentityKey(existing) == nextKey {
			agents[i] = next
			return agents
		}
	}
	return append(agents, next)
}

func RemovePendingTask(tasks []PendingTask, childRequestID string) []PendingTask {
	filtered := tasks[:0]
	for _, task := range tasks {
		if task.ChildRequestID == childRequestID {
			continue
		}
		filtered = append(filtered, task)
	}
	return filtered
}

func FindPendingTask(tasks []PendingTask, childRequestID string) (PendingTask, bool) {
	for _, task := range tasks {
		if task.ChildRequestID == childRequestID {
			return task, true
		}
	}
	return PendingTask{}, false
}

func NewID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(buf))
}

func mergeDefaultSettings(settings *Settings, defaults Settings) {
	runtime, err := ResolveHubRuntime(settings.HubRegion, settings.HubURL)
	if envRuntime, envErr, ok := runtimeFromEnv(); ok && envErr == nil {
		runtime = envRuntime
		err = nil
	}
	if err != nil {
		runtime, err = ResolveHubRuntime(defaults.HubRegion, defaults.HubURL)
		if err != nil {
			runtime = DefaultHubRuntime()
		}
	}
	settings.HubRegion = runtime.ID
	settings.HubURL = runtime.HubURL

	if settings.ListenAddr == "" {
		settings.ListenAddr = defaults.ListenAddr
	}
	if settings.SessionKey == "" {
		settings.SessionKey = defaults.SessionKey
	}
	if settings.PollInterval == 0 {
		settings.PollInterval = defaults.PollInterval
	}
	if settings.TaskTimeout == 0 {
		settings.TaskTimeout = defaults.TaskTimeout
	}
	if settings.GoogleAnalyticsMeasurementID == "" {
		settings.GoogleAnalyticsMeasurementID = defaults.GoogleAnalyticsMeasurementID
	}
	if dataDirOverride, ok := envValue("APP_DATA_DIR"); ok {
		settings.DataDir = dataDirOverride
	} else if settings.DataDir == "" {
		settings.DataDir = defaults.DataDir
	}
	if gaMeasurementID, ok := envValue("MOLTENHUB_GOOGLE_ANALYTICS_ID"); ok {
		settings.GoogleAnalyticsMeasurementID = gaMeasurementID
	}
}

func normalizeStateAliases(state *AppState) {
	if state == nil {
		return
	}
	normalizeSettingsAliases(&state.Settings)
	normalizeSessionAliases(&state.Session)
	normalizeConnectionAliases(&state.Connection, state.Session, state.Settings)
	normalizeFlash(&state.Flash)
	state.ScheduledMessages = normalizeScheduledMessages(state.ScheduledMessages)
}

func normalizeSettingsAliases(settings *Settings) {
	if settings == nil {
		return
	}
	runtime, err := ResolveHubRuntime(settings.HubRegion, settings.HubURL)
	if err != nil {
		runtime = DefaultHubRuntime()
	}
	settings.HubRegion = runtime.ID
	settings.HubURL = runtime.HubURL
}

func normalizeSessionAliases(session *Session) {
	if session == nil {
		return
	}
	session.AgentToken = coalesceTrimmed(session.AgentToken, session.BindToken)
	session.BindToken = coalesceTrimmed(session.BindToken, session.AgentToken)
	session.HubURL = normalizeHubRuntimeURL(session.HubURL)
	runtimeEndpoints := sanitizeRuntimeEndpoints(runtimeEndpointsFromSession(*session))
	session.ManifestURL = runtimeEndpoints.ManifestURL
	session.MetadataURL = runtimeEndpoints.MetadataURL
	session.Capabilities = runtimeEndpoints.CapabilitiesURL
	session.OpenClawPullURL = runtimeEndpoints.OpenClawPullURL
	session.OpenClawPushURL = runtimeEndpoints.OpenClawPushURL
	session.OfflineURL = runtimeEndpoints.OpenClawOfflineURL

	session.APIBase = NormalizeHubEndpointURL(coalesceTrimmed(session.APIBase, session.BaseURL))
	if session.APIBase == "" {
		session.APIBase = NormalizeHubEndpointURL(runtimeAPIBaseFromSession(*session))
	}
	session.BaseURL = session.APIBase
}

func normalizeConnectionAliases(connection *ConnectionState, session Session, settings Settings) {
	if connection == nil {
		return
	}
	connection.BaseURL, connection.Domain = hubConnectionTarget(connection.BaseURL, coalesceTrimmed(session.APIBase, settings.HubURL))
}

func normalizeFlash(flash *FlashMessage) {
	if flash == nil {
		return
	}
	flash.Level = strings.ToLower(strings.TrimSpace(flash.Level))
	flash.Message = strings.TrimSpace(flash.Message)
	switch flash.Level {
	case "error":
	default:
		if flash.Message == "" {
			flash.Level = ""
		} else {
			flash.Level = "info"
		}
	}
}

func normalizeScheduledMessages(messages []ScheduledMessage) []ScheduledMessage {
	if len(messages) == 0 {
		return nil
	}
	out := make([]ScheduledMessage, 0, len(messages))
	for _, message := range messages {
		message.ID = strings.TrimSpace(message.ID)
		if message.ID == "" {
			continue
		}
		message.Status = strings.TrimSpace(message.Status)
		if message.Status == "" {
			message.Status = ScheduledMessageStatusActive
		}
		message.ParentRequestID = strings.TrimSpace(message.ParentRequestID)
		message.OriginalSkillName = strings.TrimSpace(message.OriginalSkillName)
		message.TargetAgentRef = strings.TrimSpace(message.TargetAgentRef)
		message.TargetAgentDisplayName = strings.TrimSpace(message.TargetAgentDisplayName)
		message.TargetAgentEmoji = strings.TrimSpace(message.TargetAgentEmoji)
		message.TargetAgentUUID = strings.TrimSpace(message.TargetAgentUUID)
		message.TargetAgentURI = strings.TrimSpace(message.TargetAgentURI)
		message.CallerAgentUUID = strings.TrimSpace(message.CallerAgentUUID)
		message.CallerAgentURI = strings.TrimSpace(message.CallerAgentURI)
		message.CallerRequestID = strings.TrimSpace(message.CallerRequestID)
		message.Repo = strings.TrimSpace(message.Repo)
		message.DispatchPayloadFormat = strings.TrimSpace(message.DispatchPayloadFormat)
		message.Cron = strings.TrimSpace(message.Cron)
		if !message.CreatedAt.IsZero() {
			message.CreatedAt = message.CreatedAt.UTC()
		}
		if !message.NextRunAt.IsZero() {
			message.NextRunAt = message.NextRunAt.UTC()
		}
		if !message.LastRunAt.IsZero() {
			message.LastRunAt = message.LastRunAt.UTC()
		}
		if message.Frequency < 0 {
			message.Frequency = 0
		}
		if message.Frequency == 0 && message.Cron != "" {
			message.Frequency = durationFromCron(message.Cron)
		}
		if message.Frequency > 0 && message.Cron == "" {
			message.Cron = cronFromDuration(message.Frequency)
		}
		if message.Timeout < 0 {
			message.Timeout = 0
		}
		out = append(out, message)
	}
	return out
}

func cloneState(state AppState) AppState {
	data, err := json.Marshal(state)
	if err != nil {
		return state
	}
	var clone AppState
	if err := json.Unmarshal(data, &clone); err != nil {
		return state
	}
	return clone
}

func envOrDefault(key, fallback string) string {
	value, ok := envValue(key)
	if !ok {
		return fallback
	}
	return value
}

func envValue(key string) (string, bool) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return colonEnvValue(key)
	}
	return value, true
}

func colonEnvValue(key string) (string, bool) {
	prefix := key + ":"
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, prefix) {
			continue
		}
		value := strings.TrimPrefix(entry, prefix)
		if beforeEquals, _, found := strings.Cut(value, "="); found {
			if trimmed := strings.TrimSpace(beforeEquals); trimmed != "" {
				return trimmed, true
			}
			value = strings.TrimPrefix(value, beforeEquals+"=")
		}
		if value = strings.TrimSpace(value); value != "" {
			return value, true
		}
	}
	return "", false
}

func defaultRuntimeFromEnv() HubRuntime {
	if runtime, err, ok := runtimeFromEnv(); ok && err == nil {
		return runtime
	}
	return DefaultHubRuntime()
}

func runtimeFromEnv() (HubRuntime, error, bool) {
	region, ok := envValue(moltenHubRegionEnvVar)
	if !ok {
		return HubRuntime{}, nil, false
	}
	runtime, err := ResolveHubRuntime(region, "")
	if err != nil {
		return HubRuntime{}, fmt.Errorf("%s=%q: %w", moltenHubRegionEnvVar, region, err), true
	}
	return runtime, nil, true
}

func applyPersistedConfig(state *AppState, persisted persistedConfig) {
	if state == nil {
		return
	}

	if runtime, err := ResolveHubRuntime(persisted.Settings.HubRegion, persisted.Settings.HubURL); err == nil {
		state.Settings.HubRegion = runtime.ID
		state.Settings.HubURL = runtime.HubURL
	}

	token := coalesceTrimmed(persisted.Session.AgentToken, persisted.Session.BindToken)
	state.Session.AgentToken = token
	state.Session.BindToken = token
	state.ScheduledMessages = normalizeScheduledMessages(persisted.ScheduledMessages)
}

func persistedConfigFromState(state AppState) persistedConfig {
	scheduledMessages := normalizeScheduledMessages(state.ScheduledMessages)
	for i := range scheduledMessages {
		if scheduledMessages[i].Frequency > 0 && scheduledMessages[i].Cron == "" {
			scheduledMessages[i].Cron = cronFromDuration(scheduledMessages[i].Frequency)
		}
		if scheduledMessages[i].Cron != "" {
			scheduledMessages[i].Frequency = 0
		}
	}
	return persistedConfig{
		Settings: persistedSettings{
			HubURL: normalizeHubRuntimeURL(state.Settings.HubURL),
		},
		Session: persistedSession{
			AgentToken: coalesceTrimmed(state.Session.AgentToken, state.Session.BindToken),
		},
		ScheduledMessages: scheduledMessages,
	}
}
