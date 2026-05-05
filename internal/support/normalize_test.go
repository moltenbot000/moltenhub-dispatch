package support

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCompactStrings(t *testing.T) {
	got := CompactStrings([]string{" alpha ", "", "beta", "alpha", "beta ", " gamma "})
	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSplitLinesTrimsAndPreservesOrder(t *testing.T) {
	got := SplitLines(" /tmp/a.log \n\n/tmp/b.log\n/tmp/a.log\n")
	want := []string{"/tmp/a.log", "/tmp/b.log", "/tmp/a.log"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestUnmarshalJSONPayloadAcceptsPromptWhitespace(t *testing.T) {
	raw := []byte("{\"prompt\":\"review\tlogs\nthen retry\"}")
	var payload map[string]any

	if err := UnmarshalJSONPayload(raw, &payload); err != nil {
		t.Fatalf("UnmarshalJSONPayload: %v", err)
	}
	if got := payload["prompt"]; got != "review\tlogs\nthen retry" {
		t.Fatalf("prompt = %#v", got)
	}
}

func TestUnmarshalJSONPayloadPreservesStrictJSONErrors(t *testing.T) {
	var payload map[string]any

	err := UnmarshalJSONPayload([]byte("{\"prompt\":"), &payload)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "unexpected end of JSON input") {
		t.Fatalf("unexpected parse error: %v", err)
	}
}

func TestStringSliceFromAny(t *testing.T) {
	fromStrings := StringSliceFromAny([]string{"a", "b"})
	if len(fromStrings) != 2 || fromStrings[0] != "a" || fromStrings[1] != "b" {
		t.Fatalf("unexpected []string conversion: %#v", fromStrings)
	}

	fromAny := StringSliceFromAny([]any{"a", 42, "b"})
	if len(fromAny) != 2 || fromAny[0] != "a" || fromAny[1] != "b" {
		t.Fatalf("unexpected []any conversion: %#v", fromAny)
	}
}

func TestParseDurationAcceptsMinutesHoursDaysAndSeconds(t *testing.T) {
	tests := map[string]time.Duration{
		"15m":      15 * time.Minute,
		"2h":       2 * time.Hour,
		"3d":       72 * time.Hour,
		"every 4d": 96 * time.Hour,
		"in 30m":   30 * time.Minute,
		"90":       90 * time.Second,
	}

	for raw, want := range tests {
		got, err := ParseDuration(raw)
		if err != nil {
			t.Fatalf("ParseDuration(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("ParseDuration(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestCloneMapDeepCopiesNestedMaps(t *testing.T) {
	original := map[string]any{
		"name": "worker",
		"nested": map[string]any{
			"status": "queued",
		},
	}

	cloned := CloneMap(original)
	nested := cloned["nested"].(map[string]any)
	nested["status"] = "failed"

	if original["nested"].(map[string]any)["status"] != "queued" {
		t.Fatalf("original map mutated: %#v", original)
	}
}

func TestStringFromMapAndFirstNonEmptyString(t *testing.T) {
	values := map[string]any{
		"empty": "   ",
		"name":  "  agent-1 ",
	}

	if got := StringFromMap(values, "missing", "empty", "name"); got != "agent-1" {
		t.Fatalf("StringFromMap = %q, want agent-1", got)
	}

	if got := FirstNonEmptyString("", "  ", " alpha ", "beta"); got != "alpha" {
		t.Fatalf("FirstNonEmptyString = %q, want alpha", got)
	}
}

func TestStringFromAnyAndMapByKeyTraverseNestedPayloads(t *testing.T) {
	payload := map[string]any{
		"result": map[string]any{
			"agent": map[string]any{
				"access_token": "  agent-token  ",
			},
			"endpoints": map[string]any{
				"metadata": "https://na.hub.molten.bot/runtime/profile",
			},
		},
	}

	if got := StringFromAny(payload, "agent_token", "access_token"); got != "agent-token" {
		t.Fatalf("StringFromAny = %q, want agent-token", got)
	}

	endpoints := MapByKey(payload, "endpoints")
	if got := StringFromMap(endpoints, "metadata"); got != "https://na.hub.molten.bot/runtime/profile" {
		t.Fatalf("MapByKey/StringFromMap = %q, want metadata endpoint", got)
	}
}

func TestNormalizeEmptyAndFallbackBranches(t *testing.T) {
	if got := FirstNonEmptyString(" ", "\t"); got != "" {
		t.Fatalf("FirstNonEmptyString empty = %q, want empty", got)
	}
	if got := StringFromMap(nil, "name"); got != "" {
		t.Fatalf("StringFromMap nil = %q, want empty", got)
	}
	if got := MapByKey(map[string]any{"nested": map[string]any{}}, "nested"); got != nil {
		t.Fatalf("MapByKey empty nested = %#v, want nil", got)
	}
	if got := MapByKey([]any{map[string]any{"nested": map[string]any{"name": "agent"}}}, "nested"); got["name"] != "agent" {
		t.Fatalf("MapByKey slice = %#v, want nested map", got)
	}
	if got := StringSliceFromAny("skills"); got != nil {
		t.Fatalf("StringSliceFromAny unsupported = %#v, want nil", got)
	}
	if got := CloneMap(nil); got != nil {
		t.Fatalf("CloneMap nil = %#v, want nil", got)
	}
}

func TestParseDurationRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{"", "every", "not-a-duration", "1x"} {
		if _, err := ParseDuration(raw); err == nil {
			t.Fatalf("ParseDuration(%q) expected error", raw)
		}
	}
}

func TestUnmarshalJSONPayloadEscapesControlBytesAndPreservesRetryErrors(t *testing.T) {
	var payload map[string]string
	raw := []byte{'{', '"', 'p', '"', ':', '"', 'a', 0x01, 'b', '"', '}'}
	if err := UnmarshalJSONPayload(raw, &payload); err != nil {
		t.Fatalf("UnmarshalJSONPayload control byte: %v", err)
	}
	if got := payload["p"]; got != "a\x01b" {
		t.Fatalf("payload = %q, want control byte string", got)
	}

	var out map[string]any
	err := UnmarshalJSONPayload([]byte("{\"p\":\"unterminated\n"), &out)
	if err == nil {
		t.Fatal("expected original JSON error")
	}
}

func TestCloneMapFallsBackToShallowClone(t *testing.T) {
	original := map[string]any{"bad": func() {}, "ok": "value"}
	cloned := CloneMap(original)
	if cloned["ok"] != "value" {
		t.Fatalf("CloneMap fallback = %#v, want ok value", cloned)
	}
	if _, err := json.Marshal(cloned); err == nil {
		t.Fatal("fallback clone should still contain unmarshalable function")
	}
}
