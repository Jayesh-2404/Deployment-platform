package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"deploy-platform/internal/domain"
)

var ErrNotFound = errors.New("not found")

type JSONStore struct {
	mu    sync.RWMutex
	path  string
	state state
	logs  *logBroker
}

type state struct {
	Projects    []domain.Project       `json:"projects"`
	Deployments []domain.Deployment    `json:"deployments"`
	Logs        []domain.DeploymentLog `json:"logs"`
	EnvVars     []domain.EnvVar        `json:"envVars"`
}

func NewJSONStore(path string) (*JSONStore, error) {
	store := &JSONStore{
		path:  path,
		logs:  newLogBroker(),
		state: state{},
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *JSONStore) CreateProject(input domain.CreateProjectInput) (domain.Project, error) {
	input, err := normalizeProjectInput(input)
	if err != nil {
		return domain.Project{}, err
	}

	now := time.Now().UTC()
	project := domain.Project{
		ID:              newID("proj"),
		Name:            input.Name,
		RepoURL:         input.RepoURL,
		Branch:          input.Branch,
		HealthCheckPath: input.HealthCheckPath,
		LiveURL:         input.LiveURL,
		Host:            input.Host,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Projects = append(s.state.Projects, project)
	return project, s.saveLocked()
}

func (s *JSONStore) ListProjects() ([]domain.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	projects := append([]domain.Project(nil), s.state.Projects...)
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].CreatedAt.After(projects[j].CreatedAt)
	})
	return projects, nil
}

func (s *JSONStore) GetProject(id string) (domain.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, project := range s.state.Projects {
		if project.ID == id {
			return project, nil
		}
	}
	return domain.Project{}, ErrNotFound
}

func (s *JSONStore) UpdateProject(project domain.Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.state.Projects {
		if s.state.Projects[index].ID == project.ID {
			project.UpdatedAt = time.Now().UTC()
			s.state.Projects[index] = project
			return s.saveLocked()
		}
	}
	return ErrNotFound
}

func (s *JSONStore) CreateDeployment(projectID string, triggeredBy string, rollbackTo string) (domain.Deployment, error) {
	if _, err := s.GetProject(projectID); err != nil {
		return domain.Deployment{}, err
	}
	if triggeredBy == "" {
		triggeredBy = "manual"
	}

	now := time.Now().UTC()
	deployment := domain.Deployment{
		ID:          newID("dep"),
		ProjectID:   projectID,
		CommitSHA:   shortID(),
		ImageTag:    "",
		Status:      domain.StatusQueued,
		StartedAt:   now,
		TriggeredBy: triggeredBy,
		RollbackTo:  rollbackTo,
	}
	if rollbackTo != "" {
		deployment.Status = domain.StatusRollback
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Deployments = append(s.state.Deployments, deployment)
	return deployment, s.saveLocked()
}

func (s *JSONStore) ListDeployments(projectID string) ([]domain.Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var deployments []domain.Deployment
	for _, deployment := range s.state.Deployments {
		if deployment.ProjectID == projectID {
			deployments = append(deployments, deployment)
		}
	}
	sort.Slice(deployments, func(i, j int) bool {
		return deployments[i].StartedAt.After(deployments[j].StartedAt)
	})
	return deployments, nil
}

func (s *JSONStore) GetDeployment(id string) (domain.Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, deployment := range s.state.Deployments {
		if deployment.ID == id {
			return deployment, nil
		}
	}
	return domain.Deployment{}, ErrNotFound
}

func (s *JSONStore) UpdateDeployment(deployment domain.Deployment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.state.Deployments {
		if s.state.Deployments[index].ID == deployment.ID {
			s.state.Deployments[index] = deployment
			return s.saveLocked()
		}
	}
	return ErrNotFound
}

func (s *JSONStore) FindPreviousSuccessfulDeployment(projectID string, currentDeploymentID string) (domain.Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var candidates []domain.Deployment
	for _, deployment := range s.state.Deployments {
		if deployment.ProjectID == projectID && deployment.ID != currentDeploymentID && deployment.Status == domain.StatusSuccess {
			candidates = append(candidates, deployment)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].StartedAt.After(candidates[j].StartedAt)
	})
	if len(candidates) == 0 {
		return domain.Deployment{}, ErrNotFound
	}
	return candidates[0], nil
}

func (s *JSONStore) AddLog(deploymentID string, stream string, message string) (domain.DeploymentLog, error) {
	logEntry := domain.DeploymentLog{
		ID:           newID("log"),
		DeploymentID: deploymentID,
		Timestamp:    time.Now().UTC(),
		Stream:       stream,
		Message:      message,
	}

	s.mu.Lock()
	s.state.Logs = append(s.state.Logs, logEntry)
	err := s.saveLocked()
	s.mu.Unlock()

	if err != nil {
		return domain.DeploymentLog{}, err
	}
	s.logs.broadcast(deploymentID, logEntry)
	return logEntry, nil
}

func (s *JSONStore) ListLogs(deploymentID string) ([]domain.DeploymentLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var logs []domain.DeploymentLog
	for _, logEntry := range s.state.Logs {
		if logEntry.DeploymentID == deploymentID {
			logs = append(logs, logEntry)
		}
	}
	sort.Slice(logs, func(i, j int) bool {
		return logs[i].Timestamp.Before(logs[j].Timestamp)
	})
	return logs, nil
}

func (s *JSONStore) SubscribeLogs(deploymentID string) (<-chan domain.DeploymentLog, func()) {
	return s.logs.subscribe(deploymentID)
}

func (s *JSONStore) ListEnvVars(projectID string) ([]domain.EnvVar, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var vars []domain.EnvVar
	for _, item := range s.state.EnvVars {
		if item.ProjectID == projectID {
			vars = append(vars, item)
		}
	}
	sort.Slice(vars, func(i, j int) bool { return vars[i].Key < vars[j].Key })
	return vars, nil
}

func (s *JSONStore) UpsertEnvVar(projectID string, key string, value string) (domain.EnvVar, error) {
	if _, err := s.GetProject(projectID); err != nil {
		return domain.EnvVar{}, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return domain.EnvVar{}, fmt.Errorf("key is required")
	}
	if !isEnvKey(key) {
		return domain.EnvVar{}, fmt.Errorf("key %q is not a valid environment variable name", key)
	}

	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.state.EnvVars {
		if s.state.EnvVars[index].ProjectID == projectID && s.state.EnvVars[index].Key == key {
			s.state.EnvVars[index].Value = value
			s.state.EnvVars[index].UpdatedAt = now
			return s.state.EnvVars[index], s.saveLocked()
		}
	}
	item := domain.EnvVar{
		ID:        newID("env"),
		ProjectID: projectID,
		Key:       key,
		Value:     value,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.state.EnvVars = append(s.state.EnvVars, item)
	return item, s.saveLocked()
}

func (s *JSONStore) DeleteEnvVar(projectID string, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.state.EnvVars {
		if s.state.EnvVars[index].ProjectID == projectID && s.state.EnvVars[index].Key == key {
			s.state.EnvVars = append(s.state.EnvVars[:index], s.state.EnvVars[index+1:]...)
			return s.saveLocked()
		}
	}
	return ErrNotFound
}

func (s *JSONStore) load() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.state = state{}
		return s.save()
	}
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewDecoder(file).Decode(&s.state)
}

func (s *JSONStore) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *JSONStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	tmpPath := s.path + ".tmp"
	file, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(s.state); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

func isEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for index, r := range key {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case index > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}
