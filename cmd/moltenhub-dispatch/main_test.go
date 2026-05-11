package main

import (
	"os"
	"testing"
	"time"
)

func TestMainStartsAndStopsOnInterrupt(t *testing.T) {
	t.Setenv("APP_DATA_DIR", t.TempDir())
	t.Setenv("LISTEN_ADDR", "127.0.0.1:0")
	t.Setenv("MOLTEN_HUB_TOKEN", "")
	t.Setenv("MOLTEN_HUB_REGION", "")

	done := make(chan struct{})
	go func() {
		main()
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("find process: %v", err)
	}
	if err := process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal interrupt: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("main did not stop after interrupt")
	}
}
