package main

import (
	"context"
	"testing"
	"time"
)

func TestRunStartsAndStopsOnContextCancel(t *testing.T) {
	t.Setenv("APP_DATA_DIR", t.TempDir())
	t.Setenv("LISTEN_ADDR", "127.0.0.1:0")
	t.Setenv("MOLTEN_HUB_TOKEN", "")
	t.Setenv("MOLTEN_HUB_REGION", "")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop after context cancel")
	}
}
