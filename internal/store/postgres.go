package store

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"deploy-platform/internal/domain"

	_ "github.com/lib/pq"
)

//go:embed migrations/*.sql
var schemaFS embed.FS

var _ Store = (*PostgresStore)(nil)

// PostgresStore is the production Store, used whenever
// DEPLOY_PLATFORM_DATABASE_URL is set.
//
// Log fan-out stays in process. This is a single-binary, single-instance control
// plane on one VPS, so an in-memory broker is the right amount of machinery.
// Running multiple API replicas would require LISTEN/NOTIFY here.
//
// Placeholders are positional ($1, $2, ...) because the driver is lib/pq.
type PostgresStore struct {
	db   *sql.DB
	logs *logBroker
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db, logs: newLogBroker()}
}

// OpenPostgres connects, verifies the connection and applies pending migrations.
func OpenPostgres(dsn string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	store := NewPostgresStore(db)
	if err := store.Migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *PostgresStore) Close() error { return s.db.Close() }

// Migrate applies every embedded migration in lexical order.
//
// The schema is embedded rather than read from disk so the single binary can be
// copied to a VPS and started from any working directory. Migration files are
// written to be idempotent (IF NOT EXISTS), which makes re-applying them on every
// boot safe and removes the need for a bookkeeping table.
func (s *PostgresStore) Migrate() error {
	entries, err := schemaFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := schemaFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

const projectColumns = `id, name, repo_url, branch, health_check_path, live_url, active_deployment_id, host, created_at, updated_at`

func (s *PostgresStore) CreateProject(input domain.CreateProjectInput) (domain.Project, error) {
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
	_, err = s.db.Exec(
		`INSERT INTO projects (`+projectColumns+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		project.ID, project.Name, project.RepoURL, project.Branch, project.HealthCheckPath,
		project.LiveURL, project.ActiveDeployID, project.Host, project.CreatedAt, project.UpdatedAt,
	)
	if err != nil {
		return domain.Project{}, fmt.Errorf("insert project: %w", err)
	}
	return project, nil
}

func (s *PostgresStore) ListProjects() ([]domain.Project, error) {
	rows, err := s.db.Query("SELECT " + projectColumns + " FROM projects ORDER BY created_at DESC")
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	projects := []domain.Project{}
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return projects, nil
}

func (s *PostgresStore) GetProject(id string) (domain.Project, error) {
	row := s.db.QueryRow("SELECT "+projectColumns+" FROM projects WHERE id = $1", id)
	project, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, ErrNotFound
	}
	return project, err
}

func (s *PostgresStore) UpdateProject(project domain.Project) error {
	project.UpdatedAt = time.Now().UTC()
	result, err := s.db.Exec(
		`UPDATE projects SET name = $1, repo_url = $2, branch = $3, health_check_path = $4,
		 live_url = $5, active_deployment_id = $6, host = $7, updated_at = $8 WHERE id = $9`,
		project.Name, project.RepoURL, project.Branch, project.HealthCheckPath,
		project.LiveURL, project.ActiveDeployID, project.Host, project.UpdatedAt, project.ID,
	)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	return requireAffected(result, "project")
}

const deploymentColumns = `id, project_id, commit_sha, image_tag, status, error, started_at, finished_at, triggered_by, rollback_to, port`

func (s *PostgresStore) CreateDeployment(projectID string, triggeredBy string, rollbackTo string) (domain.Deployment, error) {
	if _, err := s.GetProject(projectID); err != nil {
		return domain.Deployment{}, err
	}
	if triggeredBy == "" {
		triggeredBy = "manual"
	}
	status := domain.StatusQueued
	if rollbackTo != "" {
		status = domain.StatusRollback
	}
	deployment := domain.Deployment{
		ID:          newID("dep"),
		ProjectID:   projectID,
		CommitSHA:   shortID(),
		Status:      status,
		StartedAt:   time.Now().UTC(),
		TriggeredBy: triggeredBy,
		RollbackTo:  rollbackTo,
	}
	_, err := s.db.Exec(
		"INSERT INTO deployments ("+deploymentColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		deployment.ID, deployment.ProjectID, deployment.CommitSHA, deployment.ImageTag,
		string(deployment.Status), deployment.Error, deployment.StartedAt, nil,
		deployment.TriggeredBy, deployment.RollbackTo, deployment.Port,
	)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("insert deployment: %w", err)
	}
	return deployment, nil
}

func (s *PostgresStore) ListDeployments(projectID string) ([]domain.Deployment, error) {
	rows, err := s.db.Query(
		"SELECT "+deploymentColumns+" FROM deployments WHERE project_id = $1 ORDER BY started_at DESC",
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	defer rows.Close()
	deployments := []domain.Deployment{}
	for rows.Next() {
		deployment, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		deployments = append(deployments, deployment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	return deployments, nil
}

func (s *PostgresStore) GetDeployment(id string) (domain.Deployment, error) {
	row := s.db.QueryRow("SELECT "+deploymentColumns+" FROM deployments WHERE id = $1", id)
	deployment, err := scanDeployment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Deployment{}, ErrNotFound
	}
	return deployment, err
}

func (s *PostgresStore) UpdateDeployment(deployment domain.Deployment) error {
	result, err := s.db.Exec(
		`UPDATE deployments SET commit_sha = $1, image_tag = $2, status = $3, error = $4,
		 started_at = $5, finished_at = $6, triggered_by = $7, rollback_to = $8, port = $9 WHERE id = $10`,
		deployment.CommitSHA, deployment.ImageTag, string(deployment.Status), deployment.Error,
		deployment.StartedAt, nullTime(deployment.FinishedAt), deployment.TriggeredBy,
		deployment.RollbackTo, deployment.Port, deployment.ID,
	)
	if err != nil {
		return fmt.Errorf("update deployment: %w", err)
	}
	return requireAffected(result, "deployment")
}

func (s *PostgresStore) FindPreviousSuccessfulDeployment(projectID string, currentDeploymentID string) (domain.Deployment, error) {
	rows, err := s.db.Query(
		"SELECT "+deploymentColumns+` FROM deployments
		 WHERE project_id = $1 AND id <> $2 AND status = $3
		 ORDER BY started_at DESC LIMIT 1`,
		projectID, currentDeploymentID, string(domain.StatusSuccess),
	)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("find previous successful deployment: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return domain.Deployment{}, fmt.Errorf("find previous successful deployment: %w", err)
		}
		return domain.Deployment{}, ErrNotFound
	}
	deployment, err := scanDeployment(rows)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Deployment{}, ErrNotFound
	}
	return deployment, err
}

func (s *PostgresStore) AddLog(deploymentID string, stream string, message string) (domain.DeploymentLog, error) {
	entry := domain.DeploymentLog{
		ID:           newID("log"),
		DeploymentID: deploymentID,
		Timestamp:    time.Now().UTC(),
		Stream:       stream,
		Message:      message,
	}
	_, err := s.db.Exec(
		`INSERT INTO deployment_logs (id, deployment_id, timestamp, stream, message)
		 VALUES ($1,$2,$3,$4,$5)`,
		entry.ID, entry.DeploymentID, entry.Timestamp, entry.Stream, entry.Message,
	)
	if err != nil {
		return domain.DeploymentLog{}, fmt.Errorf("add log: %w", err)
	}
	s.logs.broadcast(deploymentID, entry)
	return entry, nil
}

func (s *PostgresStore) ListLogs(deploymentID string) ([]domain.DeploymentLog, error) {
	rows, err := s.db.Query(
		`SELECT id, deployment_id, timestamp, stream, message
		 FROM deployment_logs WHERE deployment_id = $1 ORDER BY timestamp ASC`,
		deploymentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list logs: %w", err)
	}
	defer rows.Close()
	entries := []domain.DeploymentLog{}
	for rows.Next() {
		var entry domain.DeploymentLog
		if err := rows.Scan(&entry.ID, &entry.DeploymentID, &entry.Timestamp, &entry.Stream, &entry.Message); err != nil {
			return nil, fmt.Errorf("list logs: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list logs: %w", err)
	}
	return entries, nil
}

func (s *PostgresStore) SubscribeLogs(deploymentID string) (<-chan domain.DeploymentLog, func()) {
	return s.logs.subscribe(deploymentID)
}

const envColumns = `id, project_id, key, value, created_at, updated_at`

func (s *PostgresStore) ListEnvVars(projectID string) ([]domain.EnvVar, error) {
	rows, err := s.db.Query(
		"SELECT "+envColumns+" FROM environment_variables WHERE project_id = $1 ORDER BY key ASC",
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list env vars: %w", err)
	}
	defer rows.Close()
	vars := []domain.EnvVar{}
	for rows.Next() {
		item, err := scanEnvVar(rows)
		if err != nil {
			return nil, err
		}
		vars = append(vars, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list env vars: %w", err)
	}
	return vars, nil
}

func (s *PostgresStore) UpsertEnvVar(projectID string, key string, value string) (domain.EnvVar, error) {
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
	row := s.db.QueryRow(
		`INSERT INTO environment_variables (`+envColumns+`)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (project_id, key)
		 DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at
		 RETURNING `+envColumns,
		newID("env"), projectID, key, value, now, now,
	)
	item, err := scanEnvVar(row)
	if err != nil {
		return domain.EnvVar{}, fmt.Errorf("upsert env var: %w", err)
	}
	return item, nil
}

func (s *PostgresStore) DeleteEnvVar(projectID string, key string) error {
	result, err := s.db.Exec("DELETE FROM environment_variables WHERE project_id = $1 AND key = $2", projectID, key)
	if err != nil {
		return fmt.Errorf("delete env var: %w", err)
	}
	return requireAffected(result, "env var")
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProject(row scanner) (domain.Project, error) {
	var project domain.Project
	var active sql.NullString
	err := row.Scan(
		&project.ID, &project.Name, &project.RepoURL, &project.Branch,
		&project.HealthCheckPath, &project.LiveURL, &active, &project.Host,
		&project.CreatedAt, &project.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Project{}, ErrNotFound
		}
		return domain.Project{}, fmt.Errorf("scan project: %w", err)
	}
	project.ActiveDeployID = active.String
	return project, nil
}

func scanDeployment(row scanner) (domain.Deployment, error) {
	var deployment domain.Deployment
	var status string
	var finished sql.NullTime
	err := row.Scan(
		&deployment.ID, &deployment.ProjectID, &deployment.CommitSHA, &deployment.ImageTag,
		&status, &deployment.Error, &deployment.StartedAt, &finished,
		&deployment.TriggeredBy, &deployment.RollbackTo, &deployment.Port,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Deployment{}, ErrNotFound
		}
		return domain.Deployment{}, fmt.Errorf("scan deployment: %w", err)
	}
	deployment.Status = domain.DeploymentStatus(status)
	if finished.Valid {
		value := finished.Time
		deployment.FinishedAt = &value
	}
	return deployment, nil
}

func scanEnvVar(row scanner) (domain.EnvVar, error) {
	var item domain.EnvVar
	err := row.Scan(&item.ID, &item.ProjectID, &item.Key, &item.Value, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.EnvVar{}, ErrNotFound
		}
		return domain.EnvVar{}, fmt.Errorf("scan env var: %w", err)
	}
	return item, nil
}

func requireAffected(result sql.Result, kind string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update %s: %w", kind, err)
	}
	if rows == 0 {
		return fmt.Errorf("%s %w", kind, ErrNotFound)
	}
	return nil
}

func nullTime(value *time.Time) sql.NullTime {
	if value == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *value, Valid: true}
}
