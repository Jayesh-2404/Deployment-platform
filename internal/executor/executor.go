package executor

import (
	"context"
	"fmt"
	"time"

	"deploy-platform/internal/domain"
)

type LogFunc func(stream string, message string)

type Executor interface {
	Deploy(ctx context.Context, project domain.Project, deployment domain.Deployment, log LogFunc) (string, error)
	Rollback(ctx context.Context, project domain.Project, target domain.Deployment, rollback domain.Deployment, log LogFunc) error
}

type SimulationExecutor struct{}

func NewSimulationExecutor() *SimulationExecutor {
	return &SimulationExecutor{}
}

func (e *SimulationExecutor) Deploy(ctx context.Context, project domain.Project, deployment domain.Deployment, log LogFunc) (string, error) {
	steps := []string{
		fmt.Sprintf("Cloning %s on branch %s", project.RepoURL, project.Branch),
		"Checking repository metadata",
		"Using Dockerfile-based build strategy",
		fmt.Sprintf("Building image deploy-platform/%s:%s", project.ID, deployment.CommitSHA),
		"Creating internal network route",
		"Starting candidate container",
		fmt.Sprintf("Running health check on %s", project.HealthCheckPath),
		"Health check passed",
	}

	for _, step := range steps {
		if err := pause(ctx); err != nil {
			return "", err
		}
		log("system", step)
	}

	return fmt.Sprintf("deploy-platform/%s:%s", project.ID, deployment.CommitSHA), nil
}

func (e *SimulationExecutor) Rollback(ctx context.Context, project domain.Project, target domain.Deployment, rollback domain.Deployment, log LogFunc) error {
	steps := []string{
		fmt.Sprintf("Preparing rollback for %s", project.Name),
		fmt.Sprintf("Starting previous image %s", target.ImageTag),
		fmt.Sprintf("Running health check on %s", project.HealthCheckPath),
		"Updating active route to previous deployment",
		"Rollback completed",
	}

	for _, step := range steps {
		if err := pause(ctx); err != nil {
			return err
		}
		log("system", step)
	}
	return nil
}

func pause(ctx context.Context) error {
	timer := time.NewTimer(550 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
