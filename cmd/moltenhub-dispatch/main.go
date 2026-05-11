package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/moltenbot000/moltenhub-dispatch/internal/app"
	"github.com/moltenbot000/moltenhub-dispatch/internal/hub"
	"github.com/moltenbot000/moltenhub-dispatch/internal/web"
)

func main() {
	rootCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(rootCtx); err != nil {
		log.Fatal(err)
	}
}

func run(rootCtx context.Context) error {
	settings := app.DefaultSettings()
	if err := os.MkdirAll(settings.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	storePath, err := app.ResolveStorePath(settings.DataDir)
	if err != nil {
		return fmt.Errorf("resolve state store path: %w", err)
	}

	store, err := app.NewStore(storePath, settings)
	if err != nil {
		return fmt.Errorf("initialize store: %w", err)
	}

	client := hub.NewClient(store.Snapshot().Settings.HubURL)
	service := app.NewService(store, client)
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 20*time.Second)
	if err := service.BindFromEnvIfNeeded(startupCtx); err != nil {
		log.Printf("automatic bind: %v", err)
	}
	startupCancel()

	serverUI, err := web.New(service)
	if err != nil {
		return fmt.Errorf("create web server: %w", err)
	}

	httpServer := &http.Server{
		Addr:              store.Snapshot().Settings.ListenAddr,
		Handler:           serverUI.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go service.RunHubLoop(rootCtx)
	go service.RunSchedulerLoop(rootCtx)

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("listening on %s", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- fmt.Errorf("http server failed: %w", err)
		}
		close(serverErr)
	}()

	select {
	case <-rootCtx.Done():
	case err := <-serverErr:
		return err
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()

	if err := service.MarkOffline(shutdownCtx, "process shutdown"); err != nil {
		log.Printf("mark offline: %v", err)
	}
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown http server: %v", err)
	}
	return nil
}
