package main

import (
	"context"
	"errors"
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
	settings := app.DefaultSettings()
	if err := os.MkdirAll(settings.DataDir, 0o755); err != nil {
		log.Fatalf("create data directory: %v", err)
	}

	storePath, err := app.ResolveStorePath(settings.DataDir)
	if err != nil {
		log.Fatalf("resolve state store path: %v", err)
	}

	store, err := app.NewStore(storePath, settings)
	if err != nil {
		log.Fatalf("initialize store: %v", err)
	}

	client := hub.NewClient(store.Snapshot().Settings.HubURL)
	service := app.NewService(store, client)

	serverUI, err := web.New(service)
	if err != nil {
		log.Fatalf("create web server: %v", err)
	}

	httpServer := &http.Server{
		Addr:              store.Snapshot().Settings.ListenAddr,
		Handler:           serverUI.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	rootCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go service.RunHubLoop(rootCtx)

	go func() {
		log.Printf("listening on %s", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server failed: %v", err)
		}
	}()

	<-rootCtx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()

	if err := service.MarkOffline(shutdownCtx, "process shutdown"); err != nil {
		log.Printf("mark offline: %v", err)
	}
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown http server: %v", err)
	}
}
