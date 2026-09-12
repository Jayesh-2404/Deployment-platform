package store

// V2 foundation: PostgreSQL store behind the same Store interface.
// Thin slice: connection + schema migrate + in-memory log fan-out.
// CRUD methods are stubbed and return errPostgresUnimplemented until
// each is implemented + integration-tested against docker compose postgres.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"

	"deploy-platform/internal/domain"

	_ "github.com/lib/pq"
)

var _ Store = (*PostgresStore)(nil)

type PostgresStore struct {
	db          *sql.DB
	mu          sync.Mutex
	subscribers map[string]map[chan domain.DeploymentLog]struct{}
}

func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	store := &PostgresStore{
		db:          db,
		subscribers: make(map[string]map[chan domain.DeploymentLog]struct{}),
	}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *PostgresStore) migrate(ctx context.Context) error {
	schema, err := os.ReadFile("migrations/0001_init.sql")
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, string(schema)); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func postgresUnimplemented(method string) error {
	return fmt.Errorf("postgres store: %s not yet implemented (V2 in progress)", method)
}

func (s *PostgresStore) CreateProject(input domain.CreateProjectInput) (domain.Project, error) {
	return domain.Project{}, postgresUnimplemented("CreateProject")
}

func (s *PostgresStore) ListProjects() ([]domain.Project, error) {
	return nil, postgresUnimplemented("ListProjects")
}

func (s *PostgresStore) GetProject(id string) (domain.Project, error) {
	return domain.Project{}, postgresUnimplemented("GetProject")
}

func (s *PostgresStore) UpdateProject(project domain.Project) error {
	return postgresUnimplemented("UpdateProject")
}

func (s *PostgresStore) CreateDeployment(projectID string, triggeredBy string, rollbackTo string) (domain.Deployment, error) {
	return domain.Deployment{}, postgresUnimplemented("CreateDeployment")
}

func (s *PostgresStore) ListDeployments(projectID string) ([]domain.Deployment, error) {
	return nil, postgresUnimplemented("ListDeployments")
}

func (s *PostgresStore) GetDeployment(id string) (domain.Deployment, error) {
	return domain.Deployment{}, postgresUnimplemented("GetDeployment")
}

func (s *PostgresStore) UpdateDeployment(deployment domain.Deployment) error {
	return postgresUnimplemented("UpdateDeployment")
}

func (s *PostgresStore) FindPreviousSuccessfulDeployment(projectID string, currentDeploymentID string) (domain.Deployment, error) {
	return domain.Deployment{}, postgresUnimplemented("FindPreviousSuccessfulDeployment")
}

func (s *PostgresStore) AddLog(deploymentID string, stream string, message string) (domain.DeploymentLog, error) {
	return domain.DeploymentLog{}, postgresUnimplemented("AddLog")
}

func (s *PostgresStore) ListLogs(deploymentID string) ([]domain.DeploymentLog, error) {
	return nil, postgresUnimplemented("ListLogs")
}

func (s *PostgresStore) SubscribeLogs(deploymentID string) (<-chan domain.DeploymentLog, func()) {
	channel := make(chan domain.DeploymentLog, 32)
	s.mu.Lock()
	if _, ok := s.subscribers[deploymentID]; !ok {
		s.subscribers[deploymentID] = make(map[chan domain.DeploymentLog]struct{})
	}
	s.subscribers[deploymentID][channel] = struct{}{}
	s.mu.Unlock()

	unsubscribe := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subscribers[deploymentID], channel)
		close(channel)
	}
	return channel, unsubscribe
}
