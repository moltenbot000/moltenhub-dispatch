package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
)

func (s *Service) PollOnce(ctx context.Context) error {
	state := s.store.Snapshot()
	if state.Session.AgentToken == "" {
		return nil
	}
	s.syncHubClient(state)

	message, ok, err := s.hub.PullOpenClaw(ctx, state.Session.AgentToken, 25*time.Second)
	if err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTPLong)
		return err
	}
	s.noteHubInteraction(nil, ConnectionTransportHTTPLong)
	if !ok {
		return s.expirePendingTasks(ctx)
	}

	handleErr := s.handleInboundMessage(ctx, message)
	if handleErr != nil {
		_ = s.hub.NackOpenClaw(ctx, state.Session.AgentToken, message.DeliveryID)
		return handleErr
	}
	if err := s.hub.AckOpenClaw(ctx, state.Session.AgentToken, message.DeliveryID); err != nil {
		return err
	}
	return s.expirePendingTasks(ctx)
}

func (s *Service) RunHubLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		state := s.store.Snapshot()
		if strings.TrimSpace(state.Session.AgentToken) == "" {
			if !sleepWithContext(ctx, s.pollInterval()) {
				return
			}
			continue
		}
		s.syncHubClient(state)

		if err := s.waitForHubReachable(ctx); err != nil {
			return
		}

		state = s.store.Snapshot()
		if strings.TrimSpace(state.Session.AgentToken) == "" {
			continue
		}
		if realtime, ok := s.hub.(realtimeHubClient); ok {
			fallback, err := s.runRealtimeCycle(ctx, realtime, state.Session.AgentToken, state.Settings.SessionKey)
			if err == nil {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if isUnauthorizedHubError(err) {
				return
			}
			if !fallback {
				if !sleepWithContext(ctx, s.pollInterval()) {
					return
				}
				continue
			}
			s.noteRealtimeFallback(err)
			if err := s.ensurePresenceOnline(ctx, ConnectionTransportHTTPLong); err != nil {
				if ctx.Err() != nil {
					return
				}
				if isUnauthorizedHubError(err) {
					return
				}
				if !sleepWithContext(ctx, s.pollInterval()) {
					return
				}
				continue
			}
			if err := s.runHTTPFallbackWindow(ctx, realtime); err != nil {
				return
			}
			continue
		}

		if err := s.ensurePresenceOnline(ctx, ConnectionTransportHTTPLong); err != nil {
			if ctx.Err() != nil {
				return
			}
			if isUnauthorizedHubError(err) {
				return
			}
			if !sleepWithContext(ctx, s.pollInterval()) {
				return
			}
			continue
		}
		if err := s.pollOnceWithTimeout(ctx); err != nil {
			if ctx.Err() != nil || isUnauthorizedHubError(err) {
				return
			}
			if !sleepWithContext(ctx, s.pollInterval()) {
				return
			}
			continue
		}
		if !sleepWithContext(ctx, s.pollInterval()) {
			return
		}
	}
}

func (s *Service) pollOnceWithTimeout(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	pollCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	if err := s.PollOnce(pollCtx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return ctx.Err()
}

func (s *Service) runRealtimeCycle(ctx context.Context, realtime realtimeHubClient, token, sessionKey string) (bool, error) {
	session, err := realtime.ConnectOpenClaw(ctx, token, sessionKey)
	if err != nil {
		return true, err
	}

	if err := s.ensurePresenceOnline(ctx, ConnectionTransportWebSocket); err != nil {
		_ = session.Close()
		return true, err
	}

	s.noteHubInteraction(nil, ConnectionTransportWebSocket)
	err = s.consumeRealtimeSession(ctx, session)
	if err == nil || ctx.Err() != nil {
		return false, err
	}
	if shouldFallbackToLongPoll(err) {
		return true, err
	}
	return false, err
}

func (s *Service) runHTTPFallbackWindow(ctx context.Context, realtime realtimeHubClient) error {
	window := s.wsFallbackWindow
	if window <= 0 {
		window = wsFallbackWindow
	}
	deadline := time.Now().Add(window)
	wsRetryDelay := s.wsUpgradeRetryDelay
	if wsRetryDelay <= 0 {
		wsRetryDelay = wsUpgradeRetryWindow
	}
	nextWSAttempt := time.Now().Add(wsRetryDelay)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.pollOnceWithTimeout(ctx); err != nil {
			if isUnauthorizedHubError(err) {
				return err
			}
			if !sleepWithContext(ctx, s.pollInterval()) {
				return ctx.Err()
			}
			continue
		}
		if realtime != nil && !time.Now().Before(nextWSAttempt) {
			state := s.store.Snapshot()
			token := strings.TrimSpace(state.Session.AgentToken)
			if token == "" {
				return nil
			}
			fallback, err := s.runRealtimeCycle(ctx, realtime, token, state.Settings.SessionKey)
			if err == nil || ctx.Err() != nil {
				return err
			}
			if isUnauthorizedHubError(err) {
				return err
			}
			if fallback {
				s.noteRealtimeFallback(err)
				if err := s.ensurePresenceOnline(ctx, ConnectionTransportHTTPLong); err != nil {
					if isUnauthorizedHubError(err) {
						return err
					}
					if !sleepWithContext(ctx, s.pollInterval()) {
						return ctx.Err()
					}
					nextWSAttempt = time.Now().Add(wsRetryDelay)
					continue
				}
			}
			nextWSAttempt = time.Now().Add(wsRetryDelay)
		}
		if time.Now().After(deadline) {
			return nil
		}
		if !sleepWithContext(ctx, s.pollInterval()) {
			return ctx.Err()
		}
	}
}

func (s *Service) waitForHubReachable(ctx context.Context) error {
	pinger, ok := s.hub.(hubPingClient)
	if !ok {
		return nil
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		timeout := s.hubPingCheckTimeout
		if timeout <= 0 {
			timeout = hubPingRequestTimeout
		}
		pingCtx, cancel := context.WithTimeout(ctx, timeout)
		detail, err := pinger.CheckPing(pingCtx)
		cancel()
		if err == nil {
			snapshot := s.store.Snapshot()
			if strings.TrimSpace(snapshot.Connection.Status) != ConnectionStatusConnected {
				s.noteHubPingReachable(detail)
			}
			return nil
		}

		retryDelay := s.hubPingRetryDelay
		if retryDelay <= 0 {
			retryDelay = hubPingRetryInterval
		}
		s.noteHubPingRetrying(err, retryDelay)
		if !sleepWithContext(ctx, retryDelay) {
			return ctx.Err()
		}
	}
}

func (s *Service) noteHubPingRetrying(err error, retryDelay time.Duration) {
	now := time.Now().UTC()
	_ = s.store.Update(func(state *AppState) error {
		baseURL, domain := hubConnectionTarget(state.Session.APIBase, state.Settings.HubURL)
		state.Connection = ConnectionState{
			Status:        ConnectionStatusDisconnected,
			Transport:     ConnectionTransportRetrying,
			LastChangedAt: now,
			Error:         strings.TrimSpace(err.Error()),
			Detail:        hubPingFailureDetail(err, retryDelay),
			BaseURL:       baseURL,
			Domain:        domain,
		}
		return nil
	})
}

func (s *Service) noteHubPingReachable(detail string) {
	now := time.Now().UTC()
	_ = s.store.Update(func(state *AppState) error {
		baseURL, domain := hubConnectionTarget(state.Session.APIBase, state.Settings.HubURL)
		state.Connection = ConnectionState{
			Status:        ConnectionStatusDisconnected,
			Transport:     ConnectionTransportReachable,
			LastChangedAt: now,
			Detail:        strings.TrimSpace(detail),
			BaseURL:       baseURL,
			Domain:        domain,
		}
		state.Session.OfflineMarked = false
		return nil
	})
}

func (s *Service) noteRealtimeFallback(err error) {
	now := time.Now().UTC()
	_ = s.store.Update(func(state *AppState) error {
		baseURL, domain := hubConnectionTarget(state.Session.APIBase, state.Settings.HubURL)
		detail := "WebSocket unavailable; falling back to HTTP long polling."
		if err != nil {
			detail = fmt.Sprintf("%s Error: %s", detail, strings.TrimSpace(err.Error()))
		}
		state.Connection = ConnectionState{
			Status:        ConnectionStatusDisconnected,
			Transport:     ConnectionTransportReachable,
			LastChangedAt: now,
			Error:         strings.TrimSpace(errorString(err)),
			Detail:        detail,
			BaseURL:       baseURL,
			Domain:        domain,
		}
		state.Session.OfflineMarked = false
		return nil
	})
}

func (s *Service) syncPresenceTransport(ctx context.Context, transport string) error {
	transport = normalizePresenceTransport(transport)
	if s.presenceSynced && s.presenceTransport == transport {
		return nil
	}
	return s.MarkOnline(ctx, transport)
}

func (s *Service) MarkOffline(ctx context.Context, reason string) error {
	state := s.store.Snapshot()
	if state.Session.AgentToken == "" || state.Session.OfflineMarked {
		return nil
	}
	s.syncHubClient(state)
	if err := s.hub.MarkOffline(ctx, state.Session.AgentToken, hub.OfflineRequest{
		SessionKey: state.Settings.SessionKey,
		Reason:     reason,
	}); err != nil {
		s.noteHubInteraction(err, ConnectionTransportHTTP)
		return err
	}
	s.presenceSynced = false
	s.presenceTransport = ""
	return s.store.Update(func(current *AppState) error {
		baseURL, domain := hubConnectionTarget(current.Session.APIBase, current.Settings.HubURL)
		current.Session.OfflineMarked = true
		current.Connection = ConnectionState{
			Status:        ConnectionStatusDisconnected,
			Transport:     ConnectionTransportOffline,
			LastChangedAt: time.Now().UTC(),
			Error:         strings.TrimSpace(reason),
			Detail:        strings.TrimSpace(reason),
			BaseURL:       baseURL,
			Domain:        domain,
		}
		return nil
	})
}

func (s *Service) MarkOnline(ctx context.Context, transport string) error {
	state := s.store.Snapshot()
	if strings.TrimSpace(state.Session.AgentToken) == "" {
		return nil
	}
	normalizedTransport := normalizePresenceTransport(transport)
	s.syncHubClient(state)
	profile := AgentProfile{
		DisplayName:     state.Session.DisplayName,
		Emoji:           state.Session.Emoji,
		ProfileMarkdown: state.Session.ProfileBio,
	}
	_, err := s.hub.UpdateMetadata(ctx, state.Session.AgentToken, hub.UpdateMetadataRequest{
		Metadata: buildAgentMetadata(profile, state.Settings.SessionKey, normalizedTransport),
	})
	if err != nil {
		s.noteHubInteraction(err, normalizedTransport)
		return err
	}
	s.noteHubInteraction(nil, normalizedTransport)
	s.presenceSynced = true
	s.presenceTransport = normalizedTransport
	return nil
}

func (s *Service) ensurePresenceOnline(ctx context.Context, transport string) error {
	normalizedTransport := normalizePresenceTransport(transport)
	if s.presenceSynced && s.presenceTransport == normalizedTransport {
		return nil
	}
	return s.MarkOnline(ctx, normalizedTransport)
}

func (s *Service) noteHubInteraction(err error, transport string) {
	transport = normalizePresenceTransport(transport)
	now := time.Now().UTC()
	if !hubReachable(err) {
		_ = s.store.Update(func(state *AppState) error {
			baseURL, domain := hubConnectionTarget(state.Session.APIBase, state.Settings.HubURL)
			state.Connection = ConnectionState{
				Status:        ConnectionStatusDisconnected,
				Transport:     ConnectionTransportOffline,
				LastChangedAt: now,
				Error:         strings.TrimSpace(err.Error()),
				Detail:        strings.TrimSpace(err.Error()),
				BaseURL:       baseURL,
				Domain:        domain,
			}
			return nil
		})
		return
	}

	_ = s.store.Update(func(state *AppState) error {
		baseURL, domain := hubConnectionTarget(state.Session.APIBase, state.Settings.HubURL)
		currentTransport := normalizePresenceTransport(state.Connection.Transport)
		if transport == ConnectionTransportHTTP &&
			state.Connection.Status == ConnectionStatusConnected &&
			currentTransport == ConnectionTransportWebSocket {
			transport = ConnectionTransportWebSocket
		}
		state.Connection = ConnectionState{
			Status:        ConnectionStatusConnected,
			Transport:     transport,
			LastChangedAt: now,
			BaseURL:       baseURL,
			Domain:        domain,
		}
		state.Session.OfflineMarked = false
		return nil
	})
}

func (s *Service) consumeRealtimeSession(ctx context.Context, session hub.RealtimeSession) error {
	defer session.Close()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		message, err := session.Receive(ctx)
		if err != nil {
			return err
		}

		handleErr := s.handleInboundMessage(ctx, message)
		if handleErr != nil {
			_ = session.Nack(ctx, message.DeliveryID)
			return handleErr
		}
		if err := session.Ack(ctx, message.DeliveryID); err != nil {
			return err
		}
		s.noteHubInteraction(nil, ConnectionTransportWebSocket)
		if err := s.expirePendingTasks(ctx); err != nil {
			return err
		}
	}
}

func (s *Service) pollInterval() time.Duration {
	interval := s.store.Snapshot().Settings.PollInterval
	if interval <= 0 {
		return 2 * time.Second
	}
	return interval
}

func shouldFallbackToLongPoll(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	for _, marker := range []string{
		"use of closed network connection",
		"websocket session closed",
		"connection reset by peer",
		"broken pipe",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return true
}

func isUnauthorizedHubError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *hub.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "status=401") ||
		strings.Contains(text, "status 401") ||
		strings.Contains(text, "status=403") ||
		strings.Contains(text, "status 403") ||
		strings.Contains(text, "unauthorized") ||
		strings.Contains(text, "forbidden")
}

func hubReachable(err error) bool {
	if err == nil {
		return true
	}
	var apiErr *hub.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode != http.StatusUnauthorized && apiErr.StatusCode != http.StatusForbidden
}

func sleepWithContext(ctx context.Context, wait time.Duration) bool {
	if wait <= 0 {
		wait = time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func hubPingFailureDetail(pingErr error, retryDelay time.Duration) string {
	if retryDelay <= 0 {
		retryDelay = hubPingRetryInterval
	}
	message := fmt.Sprintf("Hub endpoint ping failed; retrying every %s until live.", retryDelay)
	if pingErr == nil {
		return message
	}
	return fmt.Sprintf("%s Error: %s", message, strings.TrimSpace(pingErr.Error()))
}
