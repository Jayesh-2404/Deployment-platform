package app

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"deploy-platform/internal/domain"
	"deploy-platform/internal/store"
)

//go:embed static/*
var staticFiles embed.FS

type Server struct {
	store store.Store
	mux   *http.ServeMux
}

func NewServer(repository store.Store) http.Handler {
	server := &Server{
		store: repository,
		mux:   http.NewServeMux(),
	}
	server.routes()
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", s.health)
	s.mux.HandleFunc("GET /api/projects", s.listProjects)
	s.mux.HandleFunc("POST /api/projects", s.createProject)
	s.mux.HandleFunc("GET /api/projects/{id}", s.getProject)
	s.mux.HandleFunc("GET /api/projects/{id}/deployments", s.listDeployments)
	s.mux.HandleFunc("POST /api/projects/{id}/deployments", s.createDeployment)
	s.mux.HandleFunc("GET /api/deployments/{id}", s.getDeployment)
	s.mux.HandleFunc("GET /api/deployments/{id}/logs", s.listLogs)
	s.mux.HandleFunc("GET /api/deployments/{id}/logs/stream", s.streamLogs)
	s.mux.HandleFunc("POST /api/deployments/{id}/rollback", s.rollback)
	staticRoot, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticRoot))))
	s.mux.HandleFunc("GET /", s.index)
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
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var input domain.CreateProjectInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON body"))
		return
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
	writeJSON(w, http.StatusOK, deployments)
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
	writeJSON(w, http.StatusOK, logs)
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
		case logEntry := <-events:
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

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) || strings.Contains(err.Error(), "not found") {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func writeEvent(w http.ResponseWriter, logEntry domain.DeploymentLog) {
	payload, _ := json.Marshal(logEntry)
	_, _ = fmt.Fprintf(w, "event: log\ndata: %s\n\n", payload)
}
