package support

import (
	"encoding/json"
	"fmt"
	"strings"
)

func FirstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func StringFromMap(values map[string]any, keys ...string) string {
	if values == nil {
		return ""
	}
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		if value, ok := raw.(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func StringFromAny(value any, keys ...string) string {
	var out string
	visitAnyMap(value, func(entry map[string]any) bool {
		out = StringFromMap(entry, keys...)
		return out != ""
	})
	return out
}

func MapByKey(value any, key string) map[string]any {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}

	var out map[string]any
	visitAnyMap(value, func(entry map[string]any) bool {
		found, ok := entry[key].(map[string]any)
		if !ok || len(found) == 0 {
			return false
		}
		out = found
		return true
	})
	return out
}

func CompactStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func SplitLines(raw string) []string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// UnmarshalJSONPayload keeps strict JSON behavior, but tolerates raw control
// whitespace pasted into JSON string fields such as prompt text.
func UnmarshalJSONPayload(data []byte, out any) error {
	if err := json.Unmarshal(data, out); err != nil {
		normalized, ok := escapeStringControlWhitespace(data)
		if !ok {
			return err
		}
		if retryErr := json.Unmarshal(normalized, out); retryErr != nil {
			return err
		}
		return nil
	}
	return nil
}

func escapeStringControlWhitespace(data []byte) ([]byte, bool) {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false
	changed := false

	for _, b := range data {
		if !inString {
			if b == '"' {
				inString = true
			}
			out = append(out, b)
			continue
		}

		if escaped {
			escaped = false
			out = append(out, b)
			continue
		}

		switch b {
		case '\\':
			escaped = true
			out = append(out, b)
		case '"':
			inString = false
			out = append(out, b)
		case '\t':
			changed = true
			out = append(out, '\\', 't')
		case '\n':
			changed = true
			out = append(out, '\\', 'n')
		case '\r':
			changed = true
			out = append(out, '\\', 'r')
		default:
			if b < 0x20 {
				changed = true
				out = fmt.Appendf(out, "\\u%04x", b)
				continue
			}
			out = append(out, b)
		}
	}

	return out, changed
}

func StringSliceFromAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, len(typed))
		copy(out, typed)
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			if str, ok := entry.(string); ok {
				out = append(out, str)
			}
		}
		return out
	default:
		return nil
	}
}

func CloneMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}

	data, err := json.Marshal(value)
	if err != nil {
		return shallowCloneMap(value)
	}

	var cloned map[string]any
	if err := json.Unmarshal(data, &cloned); err != nil {
		return shallowCloneMap(value)
	}
	return cloned
}

func shallowCloneMap(value map[string]any) map[string]any {
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func visitAnyMap(value any, visit func(map[string]any) bool) bool {
	switch typed := value.(type) {
	case map[string]any:
		if visit(typed) {
			return true
		}
		for _, nestedKey := range []string{"data", "result", "agent", "payload"} {
			if nested, ok := typed[nestedKey]; ok {
				if visitAnyMap(nested, visit) {
					return true
				}
			}
		}
		for _, nested := range typed {
			if visitAnyMap(nested, visit) {
				return true
			}
		}
	case []any:
		for _, entry := range typed {
			if visitAnyMap(entry, visit) {
				return true
			}
		}
	}
	return false
}
