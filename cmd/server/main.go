// Command server runs the deployment platform control plane: the HTTP API, the
// deployment worker and the dashboard, all in one process.
//
// Everything is configured by environment variables so the same binary runs on a
// laptop with no dependencies and on a VPS with PostgreSQL, Docker and Caddy.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"deploy-platform/internal/app"
	"deploy-platform/internal/executor"
	"deploy-platform/internal/metrics"
	"deploy-platform/internal/proxy"
	"deploy-platform/internal/store"
	"deploy-platform/internal/worker"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("deploy-platform: %v", err)
	}
}

type options struct {
	addr          string
	dataPath      string
	databaseURL   string
	apiToken      string
	webhookSecret string
	executorKind  string
	docker        executor.DockerConfig
	caddy         proxy.Config
	pollInterval  time.Duration
}

func run() error {
	opts := optionsFromEnv()

	repository, closeStore, err := openStore(opts)
	if err != nil {
		return err
	}
	defer closeStore()

	registry := &metrics.Registry{}
	telemetry := app.NewTelemetry(registry)

	exec, err := buildExecutor(opts)
	if err != nil {
		return err
	}

	writer, err := buildProxy(opts)
	if err != nil {
		return err
	}

	deploymentWorker := worker.New(repository, exec, writer, telemetry, worker.Config{
		PollInterval: opts.pollInterval,
	})
	deploymentWorker.Start()
	defer deploymentWorker.Stop()

	if opts.apiToken == "" {
		log.Println("warning: DEPLOY_PLATFORM_API_TOKEN is not set, the API is open to the network")
	}

	server := &http.Server{
		Addr: opts.addr,
		Handler: app.NewServer(app.Dependencies{
			Store:         repository,
			Registry:      registry,
			Telemetry:     telemetry,
			WebhookSecret: opts.webhookSecret,
			BaseDomain:    opts.caddy.BaseDomain,
			AuthToken:     opts.apiToken,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		// No WriteTimeout: the SSE log stream is a long-lived response and would
		// be cut off by one.
		IdleTimeout: 120 * time.Second,
	}

	listenErrors := make(chan error, 1)
	go func() {
		log.Printf("deploy-platform listening on http://localhost%s (executor: %s)", opts.addr, opts.executorKind)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			listenErrors <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-listenErrors:
		return err
	case <-ctx.Done():
		log.Println("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

func optionsFromEnv() options {
	opts := options{
		addr:          env("DEPLOY_PLATFORM_ADDR", ":8080"),
		dataPath:      env("DEPLOY_PLATFORM_DATA", ".data/state.json"),
		databaseURL:   os.Getenv("DEPLOY_PLATFORM_DATABASE_URL"),
		apiToken:      os.Getenv("DEPLOY_PLATFORM_API_TOKEN"),
		webhookSecret: os.Getenv("DEPLOY_PLATFORM_WEBHOOK_SECRET"),
		// "docker" for real deployments, "simulation" for a dependency-free
		// local run. Defaults to simulation so a fresh checkout always works.
		executorKind: strings.ToLower(env("DEPLOY_PLATFORM_EXECUTOR", "simulation")),
		pollInterval: time.Duration(envInt("DEPLOY_PLATFORM_POLL_INTERVAL_SECONDS", 1)) * time.Second,
	}
	opts.docker = executor.DockerConfig{
		Network:       env("DEPLOY_PLATFORM_DOCKER_NETWORK", "deploy-platform"),
		ContainerPort: envInt("DEPLOY_PLATFORM_CONTAINER_PORT", 3000),
		HealthTimeout: time.Duration(envInt("DEPLOY_PLATFORM_HEALTH_TIMEOUT_SECONDS", 60)) * time.Second,
		MemoryLimit:   int64(envInt("DEPLOY_PLATFORM_MEMORY_LIMIT_MB", 512)) * 1024 * 1024,
		CPUPercent:    envInt("DEPLOY_PLATFORM_CPU_PERCENT", 100),
	}
	opts.caddy = proxy.Config{
		BaseDomain:    os.Getenv("DEPLOY_PLATFORM_BASE_DOMAIN"),
		DashboardHost: os.Getenv("DEPLOY_PLATFORM_DASHBOARD_HOST"),
		ACMEEmail:     os.Getenv("DEPLOY_PLATFORM_ACME_EMAIL"),
		CaddyfilePath: env("DEPLOY_PLATFORM_CADDYFILE", "/etc/caddy/Caddyfile"),
		ReloadBinary:  os.Getenv("DEPLOY_PLATFORM_CADDY_BINARY"),
		ReloadArgs:    []string{"reload", "--config", opts.caddy.CaddyfilePath, "--force"},
	}
	return opts
}

func openStore(opts options) (store.Store, func(), error) {
	if opts.databaseURL != "" {
		postgresStore, err := store.OpenPostgres(opts.databaseURL)
		if err != nil {
			return nil, nil, err
		}
		log.Println("using PostgreSQL store")
		return postgresStore, func() { _ = postgresStore.Close() }, nil
	}
	jsonStore, err := store.NewJSONStore(opts.dataPath)
	if err != nil {
		return nil, nil, err
	}
	log.Printf("using JSON store at %s", opts.dataPath)
	return jsonStore, func() {}, nil
}

func buildExecutor(opts options) (executor.Executor, error) {
	switch opts.executorKind {
	case "docker":
		if _, err := os.Stat(opts.docker.DockerBinaryOrDefault()); err != nil {
			return nil, fmt.Errorf("docker executor requested but %s is not available: %w", opts.docker.DockerBinaryOrDefault(), err)
		}
		log.Printf("using Docker executor on network %q", opts.docker.Network)
		return executor.NewDockerExecutor(
			executor.NewExecRunner(),
			executor.NewHTTPHealthChecker(),
			opts.docker,
		), nil
	case "simulation", "":
		log.Println("using simulation executor (no Docker calls will be made)")
		return executor.NewSimulationExecutor(), nil
	default:
		return nil, fmt.Errorf("unknown DEPLOY_PLATFORM_EXECUTOR %q: expected docker or simulation", opts.executorKind)
	}
}

func buildProxy(opts options) (proxy.Writer, error) {
	if opts.caddy.BaseDomain == "" {
		log.Println("no DEPLOY_PLATFORM_BASE_DOMAIN set: proxy routing disabled")
		return proxy.NoopWriter{}, nil
	}
	if opts.caddy.ReloadBinary == "" {
		opts.caddy.ReloadBinary = "caddy"
	}
	if opts.caddy.DashboardHost == "" {
		opts.caddy.DashboardHost = "deploy." + opts.caddy.BaseDomain
	}
	writer, err := proxy.NewCaddyWriter(opts.caddy)
	if err != nil {
		return nil, err
	}
	log.Printf("using Caddy proxy writer for %s", opts.caddy.CaddyfilePath)
	return writer, nil
}

func env(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		log.Printf("ignoring invalid %s=%q, using %d", key, value, fallback)
		return fallback
	}
	return parsed
}
