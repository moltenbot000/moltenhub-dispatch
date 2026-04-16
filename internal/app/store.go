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
	defaultAgentConfigDir           = ".moltenbot"
	defaultGoogleAnalyticsMeasureID = "G-BY33RFG2WB"
)

type Store struct {
	path  string
	mu    sync.RWMutex
	state AppState
}

func DefaultSettings() Settings {
	runtime, err := ResolveHubRuntime("", envOrDefault("MOLTENHUB_URL", DefaultHubRuntime().HubURL))
	if err != nil {
		runtime = DefaultHubRuntime()
	}
	return Settings{
		ListenAddr:                   envOrDefault("LISTEN_ADDR", ":8080"),
		HubRegion:                    runtime.ID,
		HubURL:                       runtime.HubURL,
		SessionKey:                   envOrDefault("MOLTENHUB_SESSION_KEY", "main"),
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

	if err := json.Unmarshal(data, &store.state); err != nil {
		return nil, fmt.Errorf("decode store: %w", err)
	}

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
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode store: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		return fmt.Errorf("write store: %w", err)
	}
	if err := syncAgentConfig(s.path, s.state.Session); err != nil {
		return err
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
			strings.EqualFold(connectedAgentDisplayName(agent), ref) {
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

type agentConfigFile struct {
	Agent agentConfig `json:"agent"`
}

type agentConfig struct {
	AgentToken string `json:"agent_token"`
	BaseURL    string `json:"base_url"`
}

func syncAgentConfig(storePath string, session Session) error {
	agent, ok := configuredAgentConfig(session)
	path := resolveAgentConfigPath(storePath)
	if path == "" {
		return nil
	}
	if !ok {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove agent config: %w", err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create agent config directory: %w", err)
	}
	data, err := json.MarshalIndent(agentConfigFile{Agent: agent}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write agent config: %w", err)
	}
	return nil
}

func configuredAgentConfig(session Session) (agentConfig, bool) {
	token := coalesceTrimmed(session.AgentToken, session.BindToken)
	baseURL := NormalizeHubEndpointURL(coalesceTrimmed(session.APIBase, session.BaseURL, runtimeAPIBaseFromSession(session)))
	if token == "" || baseURL == "" {
		return agentConfig{}, false
	}
	return agentConfig{
		AgentToken: token,
		BaseURL:    baseURL,
	}, true
}

func resolveAgentConfigPath(storePath string) string {
	storePath = strings.TrimSpace(storePath)
	if storePath == "" {
		return ""
	}
	rootDir := filepath.Dir(storePath)
	base := filepath.Base(rootDir)
	if strings.HasPrefix(base, ".") && base != "." {
		rootDir = filepath.Dir(rootDir)
	}
	return filepath.Join(rootDir, defaultAgentConfigDir, "config.json")
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
		return "", false
	}
	return value, true
}
