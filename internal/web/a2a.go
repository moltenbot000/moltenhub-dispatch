package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/app"
	"github.com/moltenbot000/moltenhub-dispatch/internal/support"
)

const (
	a2aProtocolVersion = "1.0"
	a2aJSONRPCVersion  = "2.0"
	a2aProtocolDomain  = "a2a-protocol.org"

	a2aTransportJSONRPC = "JSONRPC"
	a2aTransportHTTP    = "HTTP+JSON"

	a2aMethodSend       = "SendMessage"
	a2aMethodStream     = "SendStreamingMessage"
	a2aMethodGetTask    = "GetTask"
	a2aMethodListTasks  = "ListTasks"
	a2aMethodCancel     = "CancelTask"
	a2aMethodSubscribe  = "SubscribeToTask"
	a2aMethodPushGet    = "GetTaskPushNotificationConfig"
	a2aMethodPushCreate = "CreateTaskPushNotificationConfig"
	a2aMethodPushList   = "ListTaskPushNotificationConfigs"
	a2aMethodPushDelete = "DeleteTaskPushNotificationConfig"
	a2aMethodCard       = "GetExtendedAgentCard"

	a2aCompatMethodSend       = "message/send"
	a2aCompatMethodStream     = "message/stream"
	a2aCompatMethodGetTask    = "tasks/get"
	a2aCompatMethodCancel     = "tasks/cancel"
	a2aCompatMethodSubscribe  = "tasks/resubscribe"
	a2aCompatMethodPushGet    = "tasks/pushNotificationConfig/get"
	a2aCompatMethodPushCreate = "tasks/pushNotificationConfig/set"
	a2aCompatMethodPushList   = "tasks/pushNotificationConfig/list"
	a2aCompatMethodPushDelete = "tasks/pushNotificationConfig/delete"
	a2aCompatMethodCard       = "agent/getAuthenticatedExtendedCard"

	a2aCodeParseError        = -32700
	a2aCodeInvalidRequest    = -32600
	a2aCodeMethodNotFound    = -32601
	a2aCodeInvalidParams     = -32602
	a2aCodeInternal          = -32603
	a2aCodeTaskNotFound      = -32001
	a2aCodeTaskNotCancelable = -32002
	a2aCodePushUnsupported   = -32003
	a2aCodeUnsupported       = -32004
	a2aCodeContentType       = -32005
)

type a2aJSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      any             `json:"id"`
}

type a2aJSONRPCResponse struct {
	JSONRPC string              `json:"jsonrpc"`
	ID      any                 `json:"id"`
	Result  any                 `json:"result,omitempty"`
	Error   *a2aJSONRPCErrorObj `json:"error,omitempty"`
}

type a2aJSONRPCErrorObj struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Data    []map[string]any `json:"data,omitempty"`
}

type a2aProtocolError struct {
	code       int
	httpStatus int
	grpcStatus string
	reason     string
	message    string
	details    map[string]any
}

type a2aSendMessageRequest struct {
	Tenant   string         `json:"tenant,omitempty"`
	Config   any            `json:"configuration,omitempty"`
	Message  *a2aMessage    `json:"message"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type a2aMessage struct {
	ID             string         `json:"messageId,omitempty"`
	ContextID      string         `json:"contextId,omitempty"`
	Extensions     []string       `json:"extensions,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Parts          []a2aPart      `json:"parts"`
	ReferenceTasks []string       `json:"referenceTaskIds,omitempty"`
	Role           string         `json:"role"`
	TaskID         string         `json:"taskId,omitempty"`
}

type a2aPart struct {
	Text      *string        `json:"text,omitempty"`
	Raw       any            `json:"raw,omitempty"`
	Data      any            `json:"data,omitempty"`
	URL       string         `json:"url,omitempty"`
	Filename  string         `json:"filename,omitempty"`
	MediaType string         `json:"mediaType,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type a2aGetTaskRequest struct {
	Tenant        string `json:"tenant,omitempty"`
	ID            string `json:"id"`
	HistoryLength *int   `json:"historyLength,omitempty"`
}

type a2aListTasksRequest struct {
	Tenant               string     `json:"tenant,omitempty"`
	ContextID            string     `json:"contextId,omitempty"`
	Status               string     `json:"status,omitempty"`
	PageSize             int        `json:"pageSize,omitempty"`
	PageToken            string     `json:"pageToken,omitempty"`
	HistoryLength        *int       `json:"historyLength,omitempty"`
	StatusTimestampAfter *time.Time `json:"statusTimestampAfter,omitempty"`
	IncludeArtifacts     bool       `json:"includeArtifacts,omitempty"`
}

type a2aCancelTaskRequest struct {
	Tenant   string         `json:"tenant,omitempty"`
	ID       string         `json:"id"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func (s *Server) handleA2AWellKnownAgentCard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		writeA2ARESTError(w, a2aInvalidRequest("method_not_allowed", "method not allowed", nil))
		return
	}
	targetRef := support.FirstNonEmptyString(
		strings.TrimSpace(r.URL.Query().Get("target_agent_ref")),
		strings.TrimSpace(r.URL.Query().Get("agent")),
		strings.TrimSpace(r.URL.Query().Get("target")),
	)
	target := s.a2aTargetAgent(targetRef)
	writeA2AJSON(w, http.StatusOK, s.a2aAgentCard(r, targetRef, target))
}

func (s *Server) handleA2ARoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		writeA2AJSON(w, http.StatusOK, s.a2aAgentCard(r, "", nil))
	case http.MethodPost:
		s.handleA2AJSONRPC(w, r, "")
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
		writeA2ARESTError(w, a2aInvalidRequest("method_not_allowed", "method not allowed", nil))
	}
}

func (s *Server) handleA2ASubroutes(w http.ResponseWriter, r *http.Request) {
	targetRef, routePath, protocolErr := a2aRouteTargetAndPath(r.URL.EscapedPath())
	if protocolErr != nil {
		writeA2ARESTError(w, protocolErr)
		return
	}
	if routePath == "" {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			writeA2AJSON(w, http.StatusOK, s.a2aAgentCard(r, targetRef, s.a2aTargetAgent(targetRef)))
		case http.MethodPost:
			s.handleA2AJSONRPC(w, r, targetRef)
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
			writeA2ARESTError(w, a2aInvalidRequest("method_not_allowed", "method not allowed", nil))
		}
		return
	}

	switch {
	case routePath == "agent-card" || routePath == ".well-known/agent-card.json":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			writeA2ARESTError(w, a2aInvalidRequest("method_not_allowed", "method not allowed", nil))
			return
		}
		writeA2AJSON(w, http.StatusOK, s.a2aAgentCard(r, targetRef, s.a2aTargetAgent(targetRef)))
	case routePath == "message:send":
		s.handleA2ARESTSendMessage(w, r, targetRef)
	case routePath == "message:stream":
		writeA2ARESTError(w, a2aUnsupported("unsupported_operation", "streaming is not enabled for MoltenHub Dispatch A2A adapter", nil))
	case routePath == "tasks":
		s.handleA2ARESTListTasks(w, r)
	case routePath == "extendedAgentCard":
		s.handleA2ARESTExtendedAgentCard(w, r, targetRef)
	case strings.HasPrefix(routePath, "tasks/"):
		s.handleA2ARESTTaskSubroute(w, r, strings.TrimPrefix(routePath, "tasks/"))
	default:
		writeA2ARESTError(w, a2aMethodNotFound("route_not_found", "A2A route not found", map[string]any{"path": routePath}))
	}
}

func a2aRouteTargetAndPath(rawPath string) (string, string, *a2aProtocolError) {
	routePath := strings.Trim(strings.TrimPrefix(rawPath, "/v1/a2a/"), "/")
	if routePath == "" {
		return "", "", nil
	}
	if !strings.HasPrefix(routePath, "agents/") {
		return "", routePath, nil
	}
	tail := strings.Trim(strings.TrimPrefix(routePath, "agents/"), "/")
	if tail == "" {
		return "", "", a2aInvalidParams("invalid_target_agent", "target agent is required", nil)
	}
	parts := strings.SplitN(tail, "/", 2)
	targetRef, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", "", a2aInvalidParams("invalid_target_agent", "target agent reference is invalid", map[string]any{"error": err.Error()})
	}
	if len(parts) == 1 {
		return strings.TrimSpace(targetRef), "", nil
	}
	return strings.TrimSpace(targetRef), strings.Trim(strings.TrimPrefix(parts[1], "/"), "/"), nil
}

func (s *Server) handleA2AJSONRPC(w http.ResponseWriter, r *http.Request, targetRef string) {
	if r.Method != http.MethodPost {
		writeA2AJSONRPCError(w, nil, a2aInvalidRequest("invalid_request", "JSON-RPC requests must use POST", nil))
		return
	}
	if !a2aRequestHasJSONContent(r) {
		writeA2AJSONRPCError(w, nil, a2aContentType("unsupported_content_type", "A2A requests require application/json or application/a2a+json", map[string]any{"content_type": r.Header.Get("Content-Type")}))
		return
	}

	var req a2aJSONRPCRequest
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&req); err != nil {
		writeA2AJSONRPCError(w, nil, a2aParseError("parse_error", "invalid JSON-RPC request", map[string]any{"error": err.Error()}))
		return
	}
	if req.JSONRPC != a2aJSONRPCVersion {
		writeA2AJSONRPCError(w, req.ID, a2aInvalidRequest("invalid_request", "jsonrpc must be 2.0", nil))
		return
	}

	var result any
	var protocolErr *a2aProtocolError
	method := strings.TrimSpace(req.Method)
	switch method {
	case a2aMethodSend, a2aCompatMethodSend:
		result, protocolErr = s.a2aSendMessage(r, targetRef, req.Params)
		if protocolErr == nil {
			if method == a2aCompatMethodSend {
				result = a2aCompatEvent("task", result.(map[string]any))
			} else {
				result = map[string]any{"task": result}
			}
		}
	case a2aMethodGetTask, a2aCompatMethodGetTask:
		result, protocolErr = s.a2aGetTask(req.Params)
	case a2aMethodListTasks:
		result, protocolErr = s.a2aListTasks(req.Params)
	case a2aMethodCard, a2aCompatMethodCard:
		result = s.a2aAgentCard(r, targetRef, s.a2aTargetAgent(targetRef))
	case a2aMethodCancel, a2aCompatMethodCancel:
		protocolErr = a2aTaskNotCancelable("task_not_cancelable", "cancel is not enabled for MoltenHub Dispatch A2A adapter", nil)
	case a2aMethodStream, a2aMethodSubscribe, a2aCompatMethodStream, a2aCompatMethodSubscribe:
		protocolErr = a2aUnsupported("unsupported_operation", "streaming is not enabled for MoltenHub Dispatch A2A adapter", nil)
	case a2aMethodPushGet, a2aMethodPushCreate, a2aMethodPushList, a2aMethodPushDelete,
		a2aCompatMethodPushGet, a2aCompatMethodPushCreate, a2aCompatMethodPushList, a2aCompatMethodPushDelete:
		protocolErr = a2aPushUnsupported("push_notifications_not_supported", "push notifications are not enabled for MoltenHub Dispatch A2A adapter", nil)
	case "":
		protocolErr = a2aInvalidRequest("invalid_request", "method is required", nil)
	default:
		protocolErr = a2aMethodNotFound("method_not_found", "method not found", map[string]any{"method": req.Method})
	}
	if protocolErr != nil {
		writeA2AJSONRPCError(w, req.ID, protocolErr)
		return
	}

	writeA2AJSON(w, http.StatusOK, a2aJSONRPCResponse{
		JSONRPC: a2aJSONRPCVersion,
		ID:      req.ID,
		Result:  result,
	})
}

func (s *Server) handleA2ARESTSendMessage(w http.ResponseWriter, r *http.Request, targetRef string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeA2ARESTError(w, a2aInvalidRequest("method_not_allowed", "method not allowed", nil))
		return
	}
	if !a2aRequestHasJSONContent(r) {
		writeA2ARESTError(w, a2aContentType("unsupported_content_type", "A2A requests require application/json or application/a2a+json", map[string]any{"content_type": r.Header.Get("Content-Type")}))
		return
	}

	var req a2aSendMessageRequest
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&req); err != nil {
		writeA2ARESTError(w, a2aParseError("parse_error", "invalid JSON request", map[string]any{"error": err.Error()}))
		return
	}
	task, protocolErr := s.a2aSendMessageFromRequest(r, targetRef, req)
	if protocolErr != nil {
		writeA2ARESTError(w, protocolErr)
		return
	}
	writeA2AJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) handleA2ARESTListTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		writeA2ARESTError(w, a2aInvalidRequest("method_not_allowed", "method not allowed", nil))
		return
	}
	req := a2aListTaskRequestFromQuery(r.URL.Query())
	writeA2AJSON(w, http.StatusOK, s.a2aListTasksForRequest(req))
}

func (s *Server) handleA2ARESTExtendedAgentCard(w http.ResponseWriter, r *http.Request, targetRef string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		writeA2ARESTError(w, a2aInvalidRequest("method_not_allowed", "method not allowed", nil))
		return
	}
	writeA2AJSON(w, http.StatusOK, s.a2aAgentCard(r, targetRef, s.a2aTargetAgent(targetRef)))
}

func (s *Server) handleA2ARESTTaskSubroute(w http.ResponseWriter, r *http.Request, taskRoute string) {
	switch {
	case strings.HasSuffix(taskRoute, ":cancel"):
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeA2ARESTError(w, a2aInvalidRequest("method_not_allowed", "method not allowed", nil))
			return
		}
		writeA2ARESTError(w, a2aTaskNotCancelable("task_not_cancelable", "cancel is not enabled for MoltenHub Dispatch A2A adapter", nil))
	case strings.HasSuffix(taskRoute, ":subscribe"):
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeA2ARESTError(w, a2aInvalidRequest("method_not_allowed", "method not allowed", nil))
			return
		}
		writeA2ARESTError(w, a2aUnsupported("unsupported_operation", "streaming is not enabled for MoltenHub Dispatch A2A adapter", nil))
	case strings.Contains(taskRoute, "/pushNotificationConfigs"):
		writeA2ARESTError(w, a2aPushUnsupported("push_notifications_not_supported", "push notifications are not enabled for MoltenHub Dispatch A2A adapter", nil))
	default:
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			writeA2ARESTError(w, a2aInvalidRequest("method_not_allowed", "method not allowed", nil))
			return
		}
		taskID, err := url.PathUnescape(strings.Trim(taskRoute, "/"))
		if err != nil {
			writeA2ARESTError(w, a2aInvalidParams("invalid_task_id", "task id is invalid", map[string]any{"error": err.Error()}))
			return
		}
		task, protocolErr := s.a2aTaskByID(taskID, a2aHistoryLengthFromQuery(r.URL.Query()))
		if protocolErr != nil {
			writeA2ARESTError(w, protocolErr)
			return
		}
		writeA2AJSON(w, http.StatusOK, task)
	}
}

func (s *Server) a2aSendMessage(r *http.Request, targetRef string, params json.RawMessage) (map[string]any, *a2aProtocolError) {
	var req a2aSendMessageRequest
	if err := decodeA2AParams(params, &req); err != nil {
		return nil, a2aInvalidParams("invalid_params", "SendMessage params are invalid", map[string]any{"error": err.Error()})
	}
	return s.a2aSendMessageFromRequest(r, targetRef, req)
}

func (s *Server) a2aSendMessageFromRequest(r *http.Request, targetRef string, req a2aSendMessageRequest) (map[string]any, *a2aProtocolError) {
	dispatchReq, protocolErr := a2aDispatchRequestFromSendMessage(req, targetRef)
	if protocolErr != nil {
		return nil, protocolErr
	}
	task, err := s.service.DispatchFromUI(r.Context(), dispatchReq)
	if err != nil {
		return nil, a2aDispatchError(err)
	}
	return a2aTaskFromPendingTask(task), nil
}

func a2aDispatchRequestFromSendMessage(req a2aSendMessageRequest, targetRef string) (app.DispatchRequest, *a2aProtocolError) {
	if req.Message == nil {
		return app.DispatchRequest{}, a2aInvalidParams("missing_message", "message is required", nil)
	}

	dispatchPayload := map[string]any{}
	mergeA2AMetadata(dispatchPayload, req.Metadata)
	mergeA2AMetadata(dispatchPayload, req.Message.Metadata)
	if targetRef != "" {
		dispatchPayload["target_agent_ref"] = targetRef
	}

	payload, payloadFormat, controlFields, protocolErr := a2aPayloadFromParts(req.Message.Parts)
	if protocolErr != nil {
		return app.DispatchRequest{}, protocolErr
	}
	mergeA2AMetadata(dispatchPayload, controlFields)
	if payload != nil {
		dispatchPayload["payload"] = payload
	}
	if payloadFormat != "" {
		dispatchPayload["payload_format"] = payloadFormat
	}

	dispatchReq, err := app.DispatchRequestFromPayload(dispatchPayload)
	if err != nil {
		return app.DispatchRequest{}, a2aInvalidParams("invalid_dispatch_payload", "dispatch payload is invalid", map[string]any{"error": err.Error()})
	}
	dispatchReq.RequestID = support.FirstNonEmptyString(
		strings.TrimSpace(req.Message.TaskID),
		strings.TrimSpace(req.Message.ID),
		app.NewID("a2a"),
	)
	dispatchReq.PreferA2A = true
	return dispatchReq, nil
}

func a2aPayloadFromParts(parts []a2aPart) (any, string, map[string]any, *a2aProtocolError) {
	if len(parts) == 0 {
		return nil, "", nil, nil
	}
	controlFields := map[string]any{}
	for _, part := range parts {
		mergeA2AMetadata(controlFields, part.Metadata)
	}
	if len(parts) == 1 {
		part := parts[0]
		if part.Data != nil {
			if mapped, ok := part.Data.(map[string]any); ok {
				mergeA2AMetadata(controlFields, mapped)
				return nil, "", controlFields, nil
			}
			return part.Data, "json", controlFields, nil
		}
		if part.Text != nil {
			return strings.TrimSpace(*part.Text), "markdown", controlFields, nil
		}
		if part.Raw != nil {
			return part.Raw, "markdown", controlFields, nil
		}
		if strings.TrimSpace(part.URL) != "" {
			return a2aPartPayload(part), "json", controlFields, nil
		}
		return nil, "", controlFields, nil
	}

	partPayloads := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if payload := a2aPartPayload(part); len(payload) > 0 {
			partPayloads = append(partPayloads, payload)
		}
	}
	return map[string]any{"parts": partPayloads}, "json", controlFields, nil
}

func a2aPartPayload(part a2aPart) map[string]any {
	payload := make(map[string]any)
	if part.Text != nil {
		payload["text"] = *part.Text
	}
	if part.Raw != nil {
		payload["raw"] = part.Raw
	}
	if part.Data != nil {
		payload["data"] = part.Data
	}
	if strings.TrimSpace(part.URL) != "" {
		payload["url"] = strings.TrimSpace(part.URL)
	}
	if strings.TrimSpace(part.Filename) != "" {
		payload["filename"] = strings.TrimSpace(part.Filename)
	}
	if strings.TrimSpace(part.MediaType) != "" {
		payload["media_type"] = strings.TrimSpace(part.MediaType)
	}
	if len(part.Metadata) > 0 {
		payload["metadata"] = part.Metadata
	}
	return payload
}

func mergeA2AMetadata(dst, src map[string]any) {
	if dst == nil || len(src) == 0 {
		return
	}
	for key, value := range src {
		dst[key] = value
	}
}

func (s *Server) a2aGetTask(params json.RawMessage) (map[string]any, *a2aProtocolError) {
	var req a2aGetTaskRequest
	if err := decodeA2AParams(params, &req); err != nil {
		return nil, a2aInvalidParams("invalid_params", "GetTask params are invalid", map[string]any{"error": err.Error()})
	}
	if strings.TrimSpace(req.ID) == "" {
		return nil, a2aInvalidParams("missing_task_id", "task id is required", nil)
	}
	return s.a2aTaskByID(req.ID, req.HistoryLength)
}

func (s *Server) a2aListTasks(params json.RawMessage) (map[string]any, *a2aProtocolError) {
	var req a2aListTasksRequest
	if len(params) > 0 {
		if err := decodeA2AParams(params, &req); err != nil {
			return nil, a2aInvalidParams("invalid_params", "ListTasks params are invalid", map[string]any{"error": err.Error()})
		}
	}
	return s.a2aListTasksForRequest(req), nil
}

func (s *Server) a2aTaskByID(taskID string, historyLength *int) (map[string]any, *a2aProtocolError) {
	taskID = strings.TrimSpace(taskID)
	state := s.service.Snapshot()
	for _, pending := range state.PendingTasks {
		if a2aPendingTaskMatches(pending, taskID) {
			return a2aApplyHistoryLength(a2aTaskFromPendingTask(pending), historyLength), nil
		}
	}
	for _, scheduled := range state.ScheduledMessages {
		if a2aScheduledTaskMatches(scheduled, taskID) {
			return a2aApplyHistoryLength(a2aTaskFromScheduledMessage(scheduled), historyLength), nil
		}
	}
	for _, event := range state.RecentEvents {
		if a2aRuntimeEventIsTask(event) && strings.EqualFold(strings.TrimSpace(event.TaskID), taskID) {
			return a2aApplyHistoryLength(a2aTaskFromRuntimeEvent(event), historyLength), nil
		}
	}
	return nil, a2aTaskNotFound("task_not_found", "task not found", map[string]any{"task_id": taskID})
}

func (s *Server) a2aListTasksForRequest(req a2aListTasksRequest) map[string]any {
	state := s.service.Snapshot()
	tasks := make([]map[string]any, 0, len(state.PendingTasks)+len(state.ScheduledMessages)+len(state.RecentEvents))
	seen := make(map[string]struct{})
	appendTask := func(task map[string]any) {
		id := strings.TrimSpace(fmt.Sprint(task["id"]))
		if id == "" {
			return
		}
		if _, ok := seen[strings.ToLower(id)]; ok {
			return
		}
		if req.ContextID != "" && !strings.EqualFold(fmt.Sprint(task["contextId"]), req.ContextID) {
			return
		}
		if req.Status != "" {
			status := ""
			if statusMap, _ := task["status"].(map[string]any); statusMap != nil {
				status = strings.TrimSpace(fmt.Sprint(statusMap["state"]))
			}
			if !strings.EqualFold(status, req.Status) {
				return
			}
		}
		seen[strings.ToLower(id)] = struct{}{}
		tasks = append(tasks, a2aApplyHistoryLength(task, req.HistoryLength))
	}

	for _, pending := range state.PendingTasks {
		appendTask(a2aTaskFromPendingTask(pending))
	}
	for _, scheduled := range state.ScheduledMessages {
		appendTask(a2aTaskFromScheduledMessage(scheduled))
	}
	for _, event := range state.RecentEvents {
		if a2aRuntimeEventIsTask(event) {
			appendTask(a2aTaskFromRuntimeEvent(event))
		}
	}

	totalSize := len(tasks)
	pageSize := req.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}
	start := 0
	if req.PageToken != "" {
		if parsed, err := strconv.Atoi(req.PageToken); err == nil && parsed > 0 && parsed < len(tasks) {
			start = parsed
		}
	}
	end := start + pageSize
	if end > len(tasks) {
		end = len(tasks)
	}
	nextPageToken := ""
	if end < len(tasks) {
		nextPageToken = strconv.Itoa(end)
	}
	if start > len(tasks) {
		start = len(tasks)
	}

	return map[string]any{
		"tasks":         tasks[start:end],
		"totalSize":     totalSize,
		"pageSize":      pageSize,
		"nextPageToken": nextPageToken,
	}
}

func a2aPendingTaskMatches(task app.PendingTask, taskID string) bool {
	for _, candidate := range []string{task.ID, task.ChildRequestID, task.HubTaskID, task.ParentRequestID, task.CallerRequestID} {
		if strings.EqualFold(strings.TrimSpace(candidate), taskID) {
			return true
		}
	}
	return false
}

func a2aScheduledTaskMatches(task app.ScheduledMessage, taskID string) bool {
	for _, candidate := range []string{task.ID, task.ParentRequestID, task.CallerRequestID} {
		if strings.EqualFold(strings.TrimSpace(candidate), taskID) {
			return true
		}
	}
	return false
}

func a2aTaskFromPendingTask(task app.PendingTask) map[string]any {
	contextID := support.FirstNonEmptyString(task.ParentRequestID, task.ChildRequestID, task.ID)
	statusState := "TASK_STATE_SUBMITTED"
	if task.Status == app.PendingTaskStatusSending {
		statusState = "TASK_STATE_WORKING"
	}
	if downstreamState := strings.TrimSpace(task.DownstreamTaskState); downstreamState != "" {
		statusState = downstreamState
	}
	statusAt := task.CreatedAt
	if !task.DownstreamUpdatedAt.IsZero() {
		statusAt = task.DownstreamUpdatedAt
	}
	var statusMessage map[string]any
	if downstreamMessage := strings.TrimSpace(task.DownstreamMessage); downstreamMessage != "" {
		statusMessage = a2aAgentStatusMessage(task.ID, contextID, downstreamMessage)
	}
	metadata := a2aTaskMetadata(map[string]any{
		"hub_task_id":           task.HubTaskID,
		"parent_request_id":     task.ParentRequestID,
		"child_request_id":      task.ChildRequestID,
		"target_agent_uuid":     task.TargetAgentUUID,
		"target_agent_uri":      task.TargetAgentURI,
		"target_agent":          task.TargetAgentDisplayName,
		"skill_name":            task.OriginalSkillName,
		"repo":                  task.Repo,
		"log_path":              task.LogPath,
		"expires_at":            a2aTimeString(task.ExpiresAt),
		"downstream_status":     task.DownstreamStatus,
		"downstream_state":      task.DownstreamTaskState,
		"downstream_message":    task.DownstreamMessage,
		"downstream_updated_at": a2aTimeString(task.DownstreamUpdatedAt),
	})
	return map[string]any{
		"id":        task.ID,
		"contextId": contextID,
		"status":    a2aTaskStatus(statusState, statusAt, statusMessage),
		"history": []map[string]any{
			a2aMessageFromPayload(task.ID, contextID, task.ParentRequestID, task.DispatchPayload, task.DispatchPayloadFormat),
		},
		"metadata": metadata,
	}
}

func a2aTaskFromScheduledMessage(task app.ScheduledMessage) map[string]any {
	contextID := support.FirstNonEmptyString(task.ParentRequestID, task.ID)
	metadata := a2aTaskMetadata(map[string]any{
		"schedule_id":       task.ID,
		"parent_request_id": task.ParentRequestID,
		"target_agent_ref":  task.TargetAgentRef,
		"target_agent_uuid": task.TargetAgentUUID,
		"target_agent_uri":  task.TargetAgentURI,
		"target_agent":      task.TargetAgentDisplayName,
		"skill_name":        task.OriginalSkillName,
		"repo":              task.Repo,
		"next_run_at":       a2aTimeString(task.NextRunAt),
		"last_run_at":       a2aTimeString(task.LastRunAt),
		"frequency":         task.Frequency.String(),
	})
	return map[string]any{
		"id":        task.ID,
		"contextId": contextID,
		"status":    a2aTaskStatus("TASK_STATE_SUBMITTED", task.CreatedAt, a2aAgentStatusMessage(task.ID, contextID, "Scheduled for "+a2aTimeString(task.NextRunAt))),
		"history": []map[string]any{
			a2aMessageFromPayload(task.ID, contextID, task.ParentRequestID, task.DispatchPayload, task.DispatchPayloadFormat),
		},
		"metadata": metadata,
	}
}

func a2aTaskFromRuntimeEvent(event app.RuntimeEvent) map[string]any {
	state := "TASK_STATE_SUBMITTED"
	if strings.EqualFold(event.Level, "error") {
		state = "TASK_STATE_FAILED"
	} else if strings.Contains(strings.ToLower(event.Title), "completed") {
		state = "TASK_STATE_COMPLETED"
	}
	contextID := support.FirstNonEmptyString(event.TaskID, app.NewID("a2a-context"))
	return map[string]any{
		"id":        event.TaskID,
		"contextId": contextID,
		"status":    a2aTaskStatus(state, event.At, a2aAgentStatusMessage(event.TaskID, contextID, strings.TrimSpace(joinNonEmpty(": ", event.Title, event.Detail)))),
		"history":   []map[string]any{},
		"metadata": a2aTaskMetadata(map[string]any{
			"skill_name":        event.OriginalSkillName,
			"target_agent_uuid": event.TargetAgentUUID,
			"target_agent_uri":  event.TargetAgentURI,
			"target_agent":      event.TargetAgentDisplayName,
			"log_path":          event.LogPath,
			"level":             event.Level,
			"title":             event.Title,
			"detail":            event.Detail,
		}),
	}
}

func a2aRuntimeEventIsTask(event app.RuntimeEvent) bool {
	if strings.TrimSpace(event.TaskID) == "" {
		return false
	}
	if strings.EqualFold(event.Level, "error") {
		return true
	}
	title := strings.ToLower(strings.TrimSpace(event.Title))
	return strings.Contains(title, "completed")
}

func a2aMessageFromPayload(taskID, contextID, messageID string, payload any, payloadFormat string) map[string]any {
	part := map[string]any{}
	switch strings.ToLower(strings.TrimSpace(payloadFormat)) {
	case "markdown", "text", "text/plain":
		if text, ok := a2aTextFromPayload(payload); ok {
			part["text"] = text
		} else {
			part["data"] = payload
		}
	case "":
		if payload == nil {
			part["text"] = ""
		} else {
			part["data"] = payload
		}
	default:
		part["data"] = payload
	}
	return map[string]any{
		"messageId": support.FirstNonEmptyString(messageID, taskID),
		"taskId":    taskID,
		"contextId": contextID,
		"role":      "ROLE_USER",
		"parts":     []map[string]any{part},
	}
}

func a2aTaskStatus(state string, timestamp time.Time, message map[string]any) map[string]any {
	status := map[string]any{"state": state}
	if timestampText := a2aTimeString(timestamp); timestampText != "" {
		status["timestamp"] = timestampText
	}
	if len(message) > 0 {
		status["message"] = message
	}
	return status
}

func a2aTextFromPayload(payload any) (string, bool) {
	switch typed := payload.(type) {
	case nil:
		return "", true
	case string:
		return strings.TrimSpace(typed), true
	case map[string]any:
		for _, key := range []string{"input", "message", "text", "prompt"} {
			if value := strings.TrimSpace(stringFromAny(typed[key])); value != "" {
				return value, true
			}
		}
		return "", false
	default:
		return strings.TrimSpace(fmt.Sprint(payload)), true
	}
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func a2aAgentStatusMessage(taskID, contextID, text string) map[string]any {
	return map[string]any{
		"messageId": app.NewID("a2a-status"),
		"taskId":    taskID,
		"contextId": contextID,
		"role":      "ROLE_AGENT",
		"parts": []map[string]any{{
			"text": strings.TrimSpace(text),
		}},
	}
}

func a2aTaskMetadata(values map[string]any) map[string]any {
	clean := make(map[string]any)
	for key, value := range values {
		if strings.TrimSpace(key) == "" || value == nil {
			continue
		}
		if str, ok := value.(string); ok && strings.TrimSpace(str) == "" {
			continue
		}
		clean[key] = value
	}
	return map[string]any{"moltenhub_dispatch": clean}
}

func a2aApplyHistoryLength(task map[string]any, historyLength *int) map[string]any {
	if historyLength == nil {
		return task
	}
	history, ok := task["history"].([]map[string]any)
	if !ok {
		return task
	}
	if *historyLength <= 0 {
		task["history"] = []map[string]any{}
		return task
	}
	if *historyLength < len(history) {
		task["history"] = history[len(history)-*historyLength:]
	}
	return task
}

func (s *Server) a2aAgentCard(r *http.Request, targetRef string, target *app.ConnectedAgent) map[string]any {
	name := support.FirstNonEmptyString(
		strings.TrimSpace(s.service.Snapshot().Session.DisplayName),
		"MoltenHub Dispatch",
	)
	description := "Dispatches A2A SendMessage requests to connected Molten Hub agents."
	if target != nil {
		name = app.ConnectedAgentDisplayName(*target)
		description = "Connected Molten Hub agent routed through MoltenHub Dispatch."
	}
	endpointURL := a2aEndpointURL(r, targetRef)
	card := map[string]any{
		"name":        name,
		"description": description,
		"version":     "1.0.0",
		"provider": map[string]any{
			"organization": "Molten Bot",
			"url":          "https://molten.bot/dispatch",
		},
		"supportedInterfaces": []map[string]any{
			a2aInterface(endpointURL, a2aTransportJSONRPC),
			a2aInterface(endpointURL, a2aTransportHTTP),
		},
		"capabilities": map[string]any{
			"streaming":         false,
			"pushNotifications": false,
			"extendedAgentCard": true,
		},
		"defaultInputModes":  []string{"text/plain", "application/json"},
		"defaultOutputModes": []string{"application/json", "text/plain"},
		"skills":             s.a2aAgentCardSkills(target),
		"metadata": map[string]any{
			"harness":       "moltenhub-dispatch",
			"target_agent":  targetRef,
			"connected_via": "openclaw",
		},
	}
	return card
}

func (s *Server) a2aAgentCardSkills(target *app.ConnectedAgent) []map[string]any {
	if target != nil {
		return a2aSkillsFromConnectedAgent(*target)
	}
	state := s.service.Snapshot()
	skills := []map[string]any{
		a2aSkill("dispatch_skill_request", "Dispatch Skill Request", "Dispatch a request to a connected Molten Hub agent and return the downstream result.", []string{"dispatch", "moltenhub"}),
	}
	seen := map[string]struct{}{"dispatch_skill_request": {}}
	for _, agent := range state.ConnectedAgents {
		agentName := app.ConnectedAgentDisplayName(agent)
		for _, skill := range app.ConnectedAgentSkills(agent) {
			name := strings.TrimSpace(skill.Name)
			if name == "" {
				continue
			}
			id := a2aSkillID(support.FirstNonEmptyString(agent.AgentID, agent.Handle, agent.AgentUUID, agent.URI), name)
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			description := strings.TrimSpace(skill.Description)
			if description == "" {
				description = "Dispatch " + name + " to " + agentName + "."
			} else {
				description = "Dispatch to " + agentName + ": " + description
			}
			skills = append(skills, a2aSkill(id, name, description, []string{"connected-agent", "dispatch"}))
		}
	}
	return skills
}

func a2aSkillsFromConnectedAgent(agent app.ConnectedAgent) []map[string]any {
	rawSkills := app.ConnectedAgentSkills(agent)
	if len(rawSkills) == 0 {
		return []map[string]any{a2aSkill("dispatch_to_agent", "Dispatch To Agent", "Dispatch a request to this connected agent.", []string{"dispatch", "connected-agent"})}
	}
	skills := make([]map[string]any, 0, len(rawSkills))
	for _, skill := range rawSkills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		description := strings.TrimSpace(skill.Description)
		if description == "" {
			description = "Dispatch " + name + " to this connected agent."
		}
		skills = append(skills, a2aSkill(name, name, description, []string{"connected-agent", "dispatch"}))
	}
	return skills
}

func a2aSkill(id, name, description string, tags []string) map[string]any {
	return map[string]any{
		"id":          id,
		"name":        name,
		"description": description,
		"tags":        tags,
		"inputModes":  []string{"text/plain", "application/json"},
		"outputModes": []string{"application/json", "text/plain"},
	}
}

func a2aSkillID(agentRef, skillName string) string {
	id := strings.ToLower(strings.TrimSpace(agentRef + "_" + skillName))
	id = strings.NewReplacer(" ", "_", "/", "_", ":", "_", ".", "_").Replace(id)
	id = strings.Trim(id, "_")
	if id == "" {
		return strings.ToLower(strings.TrimSpace(skillName))
	}
	return id
}

func a2aInterface(rawURL, transport string) map[string]any {
	return map[string]any{
		"url":             rawURL,
		"protocolBinding": transport,
		"protocolVersion": a2aProtocolVersion,
	}
}

func (s *Server) a2aTargetAgent(targetRef string) *app.ConnectedAgent {
	targetRef = strings.TrimSpace(targetRef)
	if targetRef == "" {
		return nil
	}
	state := s.service.Snapshot()
	if target, ok := app.FindConnectedAgent(state.ConnectedAgents, targetRef); ok {
		return &target
	}
	return nil
}

func a2aEndpointURL(r *http.Request, targetRef string) string {
	endpointPath := "/v1/a2a"
	if targetRef != "" {
		endpointPath = path.Join(endpointPath, "agents", url.PathEscape(targetRef))
	}
	return strings.TrimRight(a2aRequestBaseURL(r), "/") + endpointPath
}

func a2aRequestBaseURL(r *http.Request) string {
	scheme := firstForwardedHeader(r, "X-Forwarded-Proto")
	if scheme == "" {
		scheme = firstForwardedHeader(r, "X-Forwarded-Scheme")
	}
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := firstForwardedHeader(r, "X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if host == "" {
		host = "localhost"
	}
	return strings.ToLower(strings.TrimSpace(scheme)) + "://" + strings.TrimSpace(host)
}

func firstForwardedHeader(r *http.Request, name string) string {
	value := strings.TrimSpace(r.Header.Get(name))
	if value == "" {
		return ""
	}
	head, _, _ := strings.Cut(value, ",")
	return strings.TrimSpace(head)
}

func a2aRequestHasJSONContent(r *http.Request) bool {
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if contentType == "" {
		return false
	}
	contentType, _, _ = strings.Cut(contentType, ";")
	switch strings.TrimSpace(contentType) {
	case "application/json", "application/a2a+json":
		return true
	default:
		return false
	}
}

func decodeA2AParams(raw json.RawMessage, out any) error {
	if len(raw) == 0 || string(raw) == "null" {
		data, _ := json.Marshal(map[string]any{})
		return json.Unmarshal(data, out)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	return decoder.Decode(out)
}

func a2aListTaskRequestFromQuery(query url.Values) a2aListTasksRequest {
	req := a2aListTasksRequest{
		ContextID: strings.TrimSpace(query.Get("contextId")),
		Status:    strings.TrimSpace(query.Get("status")),
		PageToken: strings.TrimSpace(query.Get("pageToken")),
	}
	if pageSize, err := strconv.Atoi(strings.TrimSpace(query.Get("pageSize"))); err == nil {
		req.PageSize = pageSize
	}
	if historyLength := a2aHistoryLengthFromQuery(query); historyLength != nil {
		req.HistoryLength = historyLength
	}
	if raw := strings.TrimSpace(query.Get("lastUpdatedAfter")); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			req.StatusTimestampAfter = &parsed
		}
	}
	req.IncludeArtifacts = strings.EqualFold(strings.TrimSpace(query.Get("includeArtifacts")), "true")
	return req
}

func a2aHistoryLengthFromQuery(query url.Values) *int {
	raw := strings.TrimSpace(query.Get("historyLength"))
	if raw == "" {
		return nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return &parsed
}

func writeA2AJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeA2AJSONRPCError(w http.ResponseWriter, id any, protocolErr *a2aProtocolError) {
	if protocolErr == nil {
		protocolErr = a2aInternal("internal_error", "internal error", nil)
	}
	writeA2AJSON(w, http.StatusOK, a2aJSONRPCResponse{
		JSONRPC: a2aJSONRPCVersion,
		ID:      id,
		Error: &a2aJSONRPCErrorObj{
			Code:    protocolErr.code,
			Message: protocolErr.message,
			Data:    a2aErrorDetails(protocolErr),
		},
	})
}

func writeA2ARESTError(w http.ResponseWriter, protocolErr *a2aProtocolError) {
	if protocolErr == nil {
		protocolErr = a2aInternal("internal_error", "internal error", nil)
	}
	writeA2AJSON(w, protocolErr.httpStatus, map[string]any{
		"error": map[string]any{
			"code":    protocolErr.httpStatus,
			"status":  protocolErr.grpcStatus,
			"message": protocolErr.message,
			"details": a2aErrorDetails(protocolErr),
		},
	})
}

func a2aErrorDetails(protocolErr *a2aProtocolError) []map[string]any {
	metadata := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	if protocolErr.details != nil {
		for _, key := range []string{"code", "task_id", "target_agent_ref", "method", "path"} {
			if value, ok := protocolErr.details[key]; ok {
				metadata[key] = fmt.Sprint(value)
			}
		}
	}
	details := []map[string]any{
		{
			"@type":    "type.googleapis.com/google.rpc.ErrorInfo",
			"reason":   protocolErr.reason,
			"domain":   a2aProtocolDomain,
			"metadata": metadata,
		},
	}
	if len(protocolErr.details) > 0 {
		structDetail := map[string]any{"@type": "type.googleapis.com/google.protobuf.Struct"}
		for key, value := range protocolErr.details {
			structDetail[key] = value
		}
		details = append(details, structDetail)
	}
	return details
}

func a2aParseError(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeParseError, http.StatusBadRequest, "INVALID_ARGUMENT", "PARSE_ERROR", code, message, details)
}

func a2aInvalidRequest(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeInvalidRequest, http.StatusBadRequest, "INVALID_ARGUMENT", "INVALID_REQUEST", code, message, details)
}

func a2aMethodNotFound(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeMethodNotFound, http.StatusNotFound, "NOT_FOUND", "METHOD_NOT_FOUND", code, message, details)
}

func a2aInvalidParams(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeInvalidParams, http.StatusBadRequest, "INVALID_ARGUMENT", "INVALID_PARAMS", code, message, details)
}

func a2aInternal(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeInternal, http.StatusInternalServerError, "INTERNAL", "INTERNAL_ERROR", code, message, details)
}

func a2aTaskNotFound(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeTaskNotFound, http.StatusNotFound, "NOT_FOUND", "TASK_NOT_FOUND", code, message, details)
}

func a2aTaskNotCancelable(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeTaskNotCancelable, http.StatusBadRequest, "FAILED_PRECONDITION", "TASK_NOT_CANCELABLE", code, message, details)
}

func a2aPushUnsupported(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodePushUnsupported, http.StatusBadRequest, "FAILED_PRECONDITION", "PUSH_NOTIFICATION_NOT_SUPPORTED", code, message, details)
}

func a2aUnsupported(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeUnsupported, http.StatusBadRequest, "FAILED_PRECONDITION", "UNSUPPORTED_OPERATION", code, message, details)
}

func a2aContentType(code, message string, details map[string]any) *a2aProtocolError {
	return a2aError(a2aCodeContentType, http.StatusBadRequest, "INVALID_ARGUMENT", "CONTENT_TYPE_NOT_SUPPORTED", code, message, details)
}

func a2aError(jsonrpcCode, httpStatus int, grpcStatus, reason, code, message string, details map[string]any) *a2aProtocolError {
	if details == nil {
		details = map[string]any{}
	}
	if strings.TrimSpace(code) != "" {
		details["code"] = strings.TrimSpace(code)
	}
	if strings.TrimSpace(message) == "" {
		message = "A2A request failed."
	}
	details["Failure"] = message
	details["Failure:"] = message
	if _, ok := details["Error details"]; !ok {
		details["Error details"] = message
	}
	if _, ok := details["Error details:"]; !ok {
		details["Error details:"] = details["Error details"]
	}
	return &a2aProtocolError{
		code:       jsonrpcCode,
		httpStatus: httpStatus,
		grpcStatus: grpcStatus,
		reason:     reason,
		message:    message,
		details:    details,
	}
}

func a2aDispatchError(err error) *a2aProtocolError {
	errText := strings.TrimSpace(err.Error())
	details := map[string]any{
		"error":          errText,
		"Error details":  errText,
		"Error details:": errText,
	}
	switch {
	case strings.Contains(errText, app.DispatchSelectionRequiredMessage),
		strings.Contains(errText, "no connected agent"),
		strings.Contains(errText, "skill_name is required"),
		strings.Contains(errText, "does not expose a default skill"):
		return a2aInvalidParams("dispatch_failed", "dispatch request failed", details)
	default:
		return a2aInternal("dispatch_failed", "dispatch request failed", details)
	}
}

func a2aCompatEvent(kind string, event map[string]any) map[string]any {
	out := make(map[string]any, len(event)+1)
	out["kind"] = kind
	for key, value := range event {
		out[key] = value
	}
	return out
}

func a2aTimeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
