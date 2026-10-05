package domain

import (
	"strings"
	"time"
)

type Project struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	RepoURL         string `json:"repoUrl"`
	Branch          string `json:"branch"`
	HealthCheckPath string `json:"healthCheckPath"`
	LiveURL         string `json:"liveUrl"`
	ActiveDeployID  string `json:"activeDeploymentId"`
	// Host is the proxy hostname that routes to this project, e.g.
	// "my-app.example.com". Kept separate from LiveURL because the URL may be
	// an http:// preview while the proxy host is always the stable name.
	Host      string    `json:"host,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
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
	// Port is the loopback port the candidate container was published on, or
	// empty while the deployment has not started a container yet. It is stored
	// per deployment because a candidate and the running previous version are
	// live at the same time, so a project-level port cannot work.
	Port int `json:"port,omitempty"`
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
	// LiveURL is optional. The API fills it from the configured base domain;
	// the store derives one when empty so callers outside the API stay simple.
	LiveURL string `json:"liveUrl"`
	// Host is optional and normally derived from LiveURL by the store.
	Host string `json:"host,omitempty"`
}

type EnvVar struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Slugify converts an arbitrary project name into a hostname-safe label.
// It is shared by the store (live URL derivation) and the executor
// (container, image and route naming) so both always agree.
func Slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}
