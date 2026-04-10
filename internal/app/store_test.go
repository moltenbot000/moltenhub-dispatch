package app

import "testing"

func TestSelectFailureReviewerUsesFirstFlaggedAgent(t *testing.T) {
	t.Parallel()

	state := AppState{
		ConnectedAgents: []ConnectedAgent{
			{ID: "worker-a"},
			{ID: "reviewer-a", FailureReviewer: true},
			{ID: "reviewer-b", FailureReviewer: true},
		},
	}

	reviewer, ok := SelectFailureReviewer(state)
	if !ok {
		t.Fatal("expected a failure reviewer")
	}
	if reviewer.ID != "reviewer-a" {
		t.Fatalf("expected first flagged reviewer, got %q", reviewer.ID)
	}
}
