package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Service) writeTaskLog(path string, payload any) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create task log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open task log: %w", err)
	}
	defer file.Close()

	entry := map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"payload":   payload,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode task log entry: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write task log entry: %w", err)
	}
	return nil
}

func (s *Service) logEvent(level, title, detail, taskID, logPath string) error {
	return s.store.AppendEvent(RuntimeEvent{
		At:      time.Now().UTC(),
		Level:   level,
		Title:   title,
		Detail:  detail,
		TaskID:  taskID,
		LogPath: logPath,
	})
}

func (s *Service) logTaskAliasEvent(level, title, detail, taskID, hubTaskID, childRequestID, logPath string) error {
	return s.store.AppendEvent(RuntimeEvent{
		At:             time.Now().UTC(),
		Level:          level,
		Title:          title,
		Detail:         detail,
		TaskID:         strings.TrimSpace(taskID),
		HubTaskID:      strings.TrimSpace(hubTaskID),
		ChildRequestID: strings.TrimSpace(childRequestID),
		LogPath:        strings.TrimSpace(logPath),
	})
}

func (s *Service) logTaskEvent(level, title, detail string, task PendingTask) error {
	return s.store.AppendEvent(RuntimeEvent{
		At:                     time.Now().UTC(),
		Level:                  level,
		Title:                  title,
		Detail:                 detail,
		TaskID:                 task.ID,
		HubTaskID:              task.HubTaskID,
		ChildRequestID:         task.ChildRequestID,
		LogPath:                task.LogPath,
		OriginalSkillName:      task.OriginalSkillName,
		TargetAgentDisplayName: task.TargetAgentDisplayName,
		TargetAgentEmoji:       coalesceTrimmed(task.TargetAgentEmoji, "🤖"),
		TargetAgentUUID:        task.TargetAgentUUID,
		TargetAgentURI:         task.TargetAgentURI,
	})
}
