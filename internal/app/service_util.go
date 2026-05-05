package app

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/moltenbot000/moltenhub-dispatch/internal/support"
)

func boolPtr(value bool) *bool {
	return &value
}

func firstMapValue(values map[string]any, keys ...string) any {
	if values == nil {
		return nil
	}
	for _, key := range keys {
		value, ok := values[key]
		if ok {
			return value
		}
	}
	return nil
}

func boolFromAny(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "y":
			return true, true
		case "false", "0", "no", "n":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func intFromAny(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case float32:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func stringFromMap(values map[string]any, keys ...string) string {
	return support.StringFromMap(values, keys...)
}

func mapFromAny(value any) map[string]any {
	mapped, _ := value.(map[string]any)
	return mapped
}

func fallbackRepo(repo string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "."
	}
	return repo
}

func coalesceTrimmed(values ...string) string {
	return support.FirstNonEmptyString(values...)
}
