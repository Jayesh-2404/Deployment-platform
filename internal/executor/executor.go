// Package executor performs the infrastructure half of a deployment.
//
// The boundary rule of this project is that the API decides, the worker
// transitions, and an Executor performs. An Executor therefore never writes
// state and never changes a deployment status itself. It reports what it is
// doing through Report and returns a result; the worker decides what that means
// for the deployment row and for the proxy.
package executor

import (
	"context"
	"strings"

	"deploy-platform/internal/domain"
)

// Report tells the worker which status the deployment has entered and emits a
// human-readable line for the deployment log.
//
// Status is domain.DeploymentStatus. Passing an empty status means "log this
// without changing the status", which is what the majority of build and health
// check output needs.
type Report func(status domain.DeploymentStatus, message string)

type DeployRequest struct {
	Project    domain.Project
	Deployment domain.Deployment
	EnvVars    []domain.EnvVar
}

// DeployResult describes what the executor actually created, so the worker can
// persist the facts routing and rollback will need later.
type DeployResult struct {
	ImageTag  string
	Container string
	// Port is the loopback port the container was published on.
	Port int
}

type RollbackRequest struct {
	Project domain.Project
	// Target is the previous successful deployment being restored.
	Target   domain.Deployment
	Rollback domain.Deployment
	EnvVars  []domain.EnvVar
}

// RollbackResult describes the container started by a rollback.
type RollbackResult struct {
	Container string
	Port      int
}

type Executor interface {
	Deploy(ctx context.Context, req DeployRequest, report Report) (DeployResult, error)
	Rollback(ctx context.Context, req RollbackRequest, report Report) (RollbackResult, error)
	// Stop tears down the container for a deployment that is no longer serving
	// traffic. It must not remove images, because rollback re-runs them.
	Stop(ctx context.Context, req DeployRequest) error
}

// containerName derives a valid, stable Docker container name from a deployment
// ID. Docker rejects names with characters outside [a-zA-Z0-9_.-], so the
// separators in our IDs are normalised.
// NoopReport is used when a caller has nowhere to send progress. Every executor
// method normalises a nil Report to this, so progress reporting is always safe.
func NoopReport(domain.DeploymentStatus, string) {}

func normalizeReport(report Report) Report {
	if report == nil {
		return NoopReport
	}
	return report
}

func containerName(deploymentID string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '_', r == '.':
			return '-'
		default:
			return -1
		}
	}, deploymentID)
	if cleaned == "" {
		cleaned = "unknown"
	}
	return "dp-" + cleaned
}

// imageTag builds the image reference for a project at a commit. Docker
// repository names must be lowercase, hence the slug.
func imageTag(project domain.Project, commitSHA string) string {
	sha := strings.ToLower(strings.TrimSpace(commitSHA))
	if len(sha) > 12 {
		sha = sha[:12]
	}
	if sha == "" {
		sha = "latest"
	}
	return "deploy-platform/" + domain.Slugify(project.Name) + ":" + sha
}
