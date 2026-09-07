package domain

import "time"

type Project struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	RepoURL         string    `json:"repoUrl"`
	Branch          string    `json:"branch"`
	HealthCheckPath string    `json:"healthCheckPath"`
	LiveURL         string    `json:"liveUrl"`
	ActiveDeployID  string    `json:"activeDeploymentId"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type DeploymentStatus string

const (
	StatusQueued      DeploymentStatus = "queued"
	StatusCloning     DeploymentStatus = "cloning"
	StatusBuilding    DeploymentStatus = "building"
	StatusDeploying   DeploymentStatus = "deploying"
	StatusHealthCheck DeploymentStatus = "running_health_check"
	StatusSuccess     DeploymentStatus = "success"
	StatusFailed      DeploymentStatus = "failed"
	StatusRolledBack  DeploymentStatus = "rolled_back"
	StatusRollback    DeploymentStatus = "rollback"
)

type Deployment struct {
	ID          string           `json:"id"`
	ProjectID   string           `json:"projectId"`
	CommitSHA   string           `json:"commitSha"`
	ImageTag    string           `json:"imageTag"`
	Status      DeploymentStatus `json:"status"`
	Error       string           `json:"error"`
	StartedAt   time.Time        `json:"startedAt"`
	FinishedAt  *time.Time       `json:"finishedAt,omitempty"`
	TriggeredBy string           `json:"triggeredBy"`
	RollbackTo  string           `json:"rollbackTo,omitempty"`
}

type DeploymentLog struct {
	ID           string    `json:"id"`
	DeploymentID string    `json:"deploymentId"`
	Timestamp    time.Time `json:"timestamp"`
	Stream       string    `json:"stream"`
	Message      string    `json:"message"`
}

type CreateProjectInput struct {
	Name            string `json:"name"`
	RepoURL         string `json:"repoUrl"`
	Branch          string `json:"branch"`
	HealthCheckPath string `json:"healthCheckPath"`
}
