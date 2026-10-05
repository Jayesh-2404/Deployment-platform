package executor

import (
	"context"
	"fmt"
	"time"

	"deploy-platform/internal/domain"
)

// SimulationExecutor is the default executor. It walks the real status
// sequence on a timer so the whole control plane -- API, worker, store, log
// streaming, rollback -- is exercisable on a machine with no Docker daemon and
// no registry access.
//
// It deliberately shares the Report contract with DockerExecutor so swapping it
// in cannot change observable behaviour beyond timing.
type SimulationExecutor struct {
	// StepDelay is how long each simulated step takes.
	StepDelay time.Duration
	// FailMode makes the simulated deployment fail at a specific stage, so the
	// failure path (previous container keeps serving, deployment marked failed)
	// can be demonstrated and tested.
	FailMode SimulationFailure
}

// SimulationFailure selects where a simulated deployment fails.
type SimulationFailure string

const (
	FailNone      SimulationFailure = ""
	FailBuild     SimulationFailure = "build"
	FailHealth    SimulationFailure = "health_check"
	FailContainer SimulationFailure = "container"
)

func NewSimulationExecutor() *SimulationExecutor {
	return &SimulationExecutor{StepDelay: 550 * time.Millisecond}
}

func (e *SimulationExecutor) step(ctx context.Context, report Report, status domain.DeploymentStatus, message string) error {
	report = normalizeReport(report)
	delay := e.StepDelay
	if delay <= 0 {
		delay = 550 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	report(status, message)
	return nil
}

func (e *SimulationExecutor) Deploy(ctx context.Context, req DeployRequest, report Report) (DeployResult, error) {
	report = normalizeReport(report)
	steps := []struct {
		status  domain.DeploymentStatus
		message string
		fail    SimulationFailure
	}{
		{domain.StatusCloning, fmt.Sprintf("Cloning %s (branch %s)", req.Project.RepoURL, req.Project.Branch), ""},
		{"", "Checking repository metadata", ""},
		{"", "Simulated Dockerfile build (no Docker daemon in this environment)", ""},
		{domain.StatusBuilding, fmt.Sprintf("Building image %s", imageTag(req.Project, "simulated")), FailBuild},
		{domain.StatusDeploying, fmt.Sprintf("Starting candidate container %s", containerName(req.Deployment.ID)), FailContainer},
		{"", "Simulated internal network route created", ""},
		{domain.StatusHealthCheck, fmt.Sprintf("Running health check on %s", req.Project.HealthCheckPath), FailHealth},
		{"", "Health check passed", ""},
	}
	for _, step := range steps {
		if err := e.step(ctx, report, step.status, step.message); err != nil {
			return DeployResult{}, err
		}
		if step.fail != "" && step.fail == e.FailMode {
			return DeployResult{}, fmt.Errorf("simulated failure during %s", step.fail)
		}
	}

	// A deterministic pseudo-port keeps the simulation stable per deployment
	// while staying inside the valid range.
	port := 20000 + int(req.Deployment.StartedAt.Unix()%20000)
	return DeployResult{
		ImageTag:  imageTag(req.Project, "simulated"),
		Container: containerName(req.Deployment.ID),
		Port:      port,
	}, nil
}

func (e *SimulationExecutor) Rollback(ctx context.Context, req RollbackRequest, report Report) (RollbackResult, error) {
	report = normalizeReport(report)
	steps := []struct {
		status  domain.DeploymentStatus
		message string
	}{
		{"", fmt.Sprintf("Preparing rollback for %s", req.Project.Name)},
		{domain.StatusDeploying, fmt.Sprintf("Restarting previous image %s", req.Target.ImageTag)},
		{domain.StatusHealthCheck, fmt.Sprintf("Running health check on %s", req.Project.HealthCheckPath)},
		{"", "Health check passed for restored version"},
	}
	for _, step := range steps {
		if err := e.step(ctx, report, step.status, step.message); err != nil {
			return RollbackResult{}, err
		}
	}
	port := 20000 + int(req.Rollback.StartedAt.Unix()%20000)
	return RollbackResult{Container: containerName(req.Rollback.ID), Port: port}, nil
}

func (e *SimulationExecutor) Stop(ctx context.Context, req DeployRequest) error {
	return e.step(ctx, nil, "", fmt.Sprintf("Removed simulated container %s", containerName(req.Deployment.ID)))
}

var _ Executor = (*SimulationExecutor)(nil)
var _ Executor = (*DockerExecutor)(nil)
