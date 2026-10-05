package app

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"deploy-platform/internal/domain"
	"deploy-platform/internal/metrics"
	"deploy-platform/internal/store"
)

//go:embed static/*
var staticFiles embed.FS

// Dependencies are the collaborators the HTTP layer needs. They are passed in
// rather than constructed here so the API stays free of infrastructure choices
// and every handler is testable with fakes.
type Dependencies struct {
	Store    store.Store
	Registry *metrics.Registry
	// Telemetry is optional; when nil no deployment metrics are emitted.
	Telemetry *Telemetry
	// WebhookSecret enables the GitHub push endpoint. When empty the endpoint
	// returns 404, so an unconfigured instance is not an open deploy trigger.
	WebhookSecret string
	// BaseDomain, when set, is used to derive project live URLs on creation.
	BaseDomain string
	// AuthToken, when set, requires every /api/* request except the health
	// check to present it as a bearer token. When empty auth is disabled, so
	// local development works with no configuration.
	AuthToken string
}

type Server struct {
	store         store.Store
	registry      *metrics.Registry
	telemetry     *Telemetry
	webhookSecret string
	baseDomain    string
	authToken     string
	mux           *http.ServeMux

	// deliveries remembers GitHub delivery IDs so a redelivered push is not
	// deployed twice. Guarded by deliveriesMu; bounded so a busy
	// installation cannot grow it without limit.
	deliveriesMu sync.Mutex
	deliveries   map[string]struct{}
}

func NewServer(deps Dependencies) http.Handler {
	registry := deps.Registry
	if registry == nil {
		registry = &metrics.Registry{}
	}
	server := &Server{
		store:         deps.Store,
		registry:      registry,
		telemetry:     deps.Telemetry,
		webhookSecret: deps.WebhookSecret,
		baseDomain:    deps.BaseDomain,
		authToken:     deps.AuthToken,
		mux:           http.NewServeMux(),
		deliveries:    make(map[string]struct{}),
	}
	server.routes()
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.requireAuth(r) && !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("unauthorized"))
		return
	}
	s.mux.ServeHTTP(w, r)
}

// requireAuth reports whether a request must present the API token. The
// health check stays open so load balancers and container orchestrators can
// probe without credentials; /metrics is a separate non-/api path and stays
// open because it only exposes deployment counts, never secrets.
func (s *Server) requireAuth(r *http.Request) bool {
	if s.authToken == "" {
		return false
	}
	if r.URL.Path == "/api/health" {
		return false
	}
	return strings.HasPrefix(r.URL.Path, "/api/")
}

// authorized checks the bearer token from the Authorization header, falling
// back to the access_token query parameter. The fallback exists for
// EventSource: browsers cannot set headers on an SSE stream, so the
// dashboard passes the token in the URL when opening the log stream.
func (s *Server) authorized(r *http.Request) bool {
	if header := r.Header.Get("Authorization"); header != "" {
		token, found := strings.CutPrefix(header, "Bearer ")
		if !found {
			return false
		}
		return subtle.ConstantTimeCompare([]byte(token), []byte(s.authToken)) == 1
	}
	if token := r.URL.Query().Get("access_token"); token != "" {
		return subtle.ConstantTimeCompare([]byte(token), []byte(s.authToken)) == 1
	}
	return false
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", s.health)
	s.mux.HandleFunc("GET /metrics", s.metrics)

	s.mux.HandleFunc("GET /api/projects", s.listProjects)
	s.mux.HandleFunc("POST /api/projects", s.createProject)
	s.mux.HandleFunc("GET /api/projects/{id}", s.getProject)
	s.mux.HandleFunc("GET /api/projects/{id}/deployments", s.listDeployments)
	s.mux.HandleFunc("POST /api/projects/{id}/deployments", s.createDeployment)
	s.mux.HandleFunc("GET /api/projects/{id}/env", s.listEnvVars)
	s.mux.HandleFunc("PUT /api/projects/{id}/env", s.upsertEnvVar)
	s.mux.HandleFunc("DELETE /api/projects/{id}/env/{key}", s.deleteEnvVar)

	s.mux.HandleFunc("GET /api/deployments/{id}", s.getDeployment)
	s.mux.HandleFunc("GET /api/deployments/{id}/logs", s.listLogs)
	s.mux.HandleFunc("GET /api/deployments/{id}/logs/stream", s.streamLogs)
	s.mux.HandleFunc("POST /api/deployments/{id}/rollback", s.rollback)

	s.mux.HandleFunc("POST /api/webhooks/github", s.githubPush)

	staticRoot, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticRoot))))
	s.mux.HandleFunc("GET /", s.index)
}

// metrics serves the Prometheus exposition format. Scrape cost is a full
// registry walk, so it is intentionally unauthenticated but only exposes
// deployment counts, never secrets.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if _, err := s.registry.WriteTo(w); err != nil {
		// Headers are already sent, so the failure can only be logged.
		return
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, staticFiles, "static/index.html")
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.ListProjects()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if s.telemetry != nil {
		s.telemetry.SetProjects(len(projects))
	}
	writeJSON(w, http.StatusOK, nonNil(projects))
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var input domain.CreateProjectInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body"))
		return
	}

	if input.LiveURL == "" && s.baseDomain != "" {
		input.LiveURL = "https://" + domain.Slugify(input.Name) + "." + s.baseDomain
	}

	project, err := s.store.CreateProject(input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, project)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.GetProject(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	deployments, err := s.store.ListDeployments(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(deployments))
}

func (s *Server) createDeployment(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	deployment, err := s.store.CreateDeployment(projectID, "manual", "")
	if err != nil {
		writeStoreError(w, err)
		return
	}
	_, _ = s.store.AddLog(deployment.ID, "system", "Deployment queued")
	writeJSON(w, http.StatusCreated, deployment)
}

func (s *Server) getDeployment(w http.ResponseWriter, r *http.Request) {
	deployment, err := s.store.GetDeployment(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, deployment)
}

func (s *Server) listLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := s.store.ListLogs(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(logs))
}

func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request) {
	deploymentID := r.PathValue("id")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	logs, err := s.store.ListLogs(deploymentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, logEntry := range logs {
		writeEvent(w, logEntry)
	}
	flusher.Flush()

	events, unsubscribe := s.store.SubscribeLogs(deploymentID)
	defer unsubscribe()

	for {
		select {
		case <-r.Context().Done():
			return
		case logEntry, ok := <-events:
			if !ok {
				return
			}
			writeEvent(w, logEntry)
			flusher.Flush()
		}
	}
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	deployment, err := s.store.GetDeployment(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	target, err := s.store.FindPreviousSuccessfulDeployment(deployment.ProjectID, deployment.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("no previous successful deployment available"))
		return
	}
	rollback, err := s.store.CreateDeployment(deployment.ProjectID, "rollback", target.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	_, _ = s.store.AddLog(rollback.ID, "system", "Rollback queued")
	writeJSON(w, http.StatusCreated, rollback)
}

// EnvVarInput is the body for setting a single environment variable.
type EnvVarInput struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (s *Server) listEnvVars(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.GetProject(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	vars, err := s.store.ListEnvVars(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(vars))
}

func (s *Server) upsertEnvVar(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if _, err := s.store.GetProject(projectID); err != nil {
		writeStoreError(w, err)
		return
	}
	var input EnvVarInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body"))
		return
	}
	item, err := s.store.UpsertEnvVar(projectID, input.Key, input.Value)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteEnvVar(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if _, err := s.store.GetProject(projectID); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.store.DeleteEnvVar(projectID, r.PathValue("key")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// nonNil returns value, or an empty slice when value is nil.
//
// Every list endpoint runs through this. A nil slice encodes as JSON `null`,
// which is not an empty array, so clients that iterate the result -- the
// dashboard does -- crash on a project that has no deployments yet. The store
// layer may legitimately return nil for "nothing found"; the HTTP contract may
// not.
func nonNil[T any](value []T) []T {
	if value == nil {
		return []T{}
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func writeEvent(w http.ResponseWriter, logEntry domain.DeploymentLog) {
	payload, _ := json.Marshal(logEntry)
	_, _ = fmt.Fprintf(w, "event: log\ndata: %s\n\n", payload)
}
