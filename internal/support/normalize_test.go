package support

import "testing"

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
