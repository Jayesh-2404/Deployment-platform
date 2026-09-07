package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"deploy-platform/internal/app"
	"deploy-platform/internal/executor"
	"deploy-platform/internal/store"
	"deploy-platform/internal/worker"
)

func main() {
	dataPath := env("DEPLOY_PLATFORM_DATA", filepath.Join(".data", "state.json"))
	addr := env("DEPLOY_PLATFORM_ADDR", ":8080")

	repository, err := store.NewJSONStore(dataPath)
	if err != nil {
		log.Fatalf("create store: %v", err)
	}

	exec := executor.NewSimulationExecutor()
	deploymentWorker := worker.New(repository, exec)
	deploymentWorker.Start()
	defer deploymentWorker.Stop()

	server := &http.Server{
		Addr:              addr,
		Handler:           app.NewServer(repository),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("deploy platform listening on http://localhost%s", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}
}

func env(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
