package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"deploy-platform/internal/domain"
)

// DockerExecutor runs a real deployment against a Docker daemon.
//
// Every external effect goes through the injected CommandRunner and the health
// check goes through the injected HealthChecker, so this entire file is
// testable without Docker installed. What cannot be unit tested is the daemon
// itself; that is the part verified on the VPS.
type DockerExecutor struct {
	runner CommandRunner
	health HealthChecker
	config DockerConfig
	builds BuildDir
	images ImagePruner
}

// DockerConfig holds the tunables that depend on the host, not the project.
type DockerConfig struct {
	// Network is the Docker network project containers attach to.
	Network string
	// ContainerPort is the port the application listens on inside the
	// container. The platform publishes it on a random loopback port.
	ContainerPort int
	// HealthTimeout is how long a candidate gets to answer its health path.
	HealthTimeout time.Duration
	// StartupGrace is how long to wait after `docker run` before the first
	// health probe, so a slow first request is not reported as a failure.
	StartupGrace time.Duration
	// MemoryLimit in bytes. Zero means no limit.
	MemoryLimit int64
	// CPUQuota as a docker --cpu-quota period ratio. Zero means no limit.
	CPUPercent int
	// GitBinary defaults to "git".
	GitBinary string
	// DockerBinary defaults to "docker".
	DockerBinary string
}

func (c DockerConfig) withDefaults() DockerConfig {
	if c.Network == "" {
		c.Network = "deploy-platform"
	}
	if c.ContainerPort == 0 {
		c.ContainerPort = 3000
	}
	if c.HealthTimeout == 0 {
		c.HealthTimeout = 60 * time.Second
	}
	if c.StartupGrace == 0 {
		c.StartupGrace = 3 * time.Second
	}
	if c.GitBinary == "" {
		c.GitBinary = "git"
	}
	if c.DockerBinary == "" {
		c.DockerBinary = "docker"
	}
	return c
}

func NewDockerExecutor(runner CommandRunner, health HealthChecker, config DockerConfig) *DockerExecutor {
	return &DockerExecutor{
		runner: runner,
		health: health,
		config: config.withDefaults(),
		builds: BuildDir{},
		images: ImagePruner{},
	}
}

// DockerBinaryOrDefault is the binary the executor shells out to.
func (c DockerConfig) DockerBinaryOrDefault() string {
	return c.withDefaults().DockerBinary
}

// BuildDir cleans up clone directories. It is a field so tests can observe that
// cleanup actually happened.
type BuildDir struct{}

// ImagePruner removes dangling images left by failed builds.
type ImagePruner struct{}

// Deploy clones, builds, starts a candidate container and gates it on its health
// endpoint.
//
// The returned DeployResult is only meaningful when the error is nil: on failure
// the candidate is torn down and the previously running container is left
// untouched, which is what makes a failed deploy safe.
func (e *DockerExecutor) Deploy(ctx context.Context, req DeployRequest, report Report) (DeployResult, error) {
	report = normalizeReport(report)
	report(domain.StatusCloning, fmt.Sprintf("Cloning %s (branch %s)", req.Project.RepoURL, req.Project.Branch))

	dir, err := e.builds.Prepare(req.Deployment.ID)
	if err != nil {
		return DeployResult{}, fmt.Errorf("prepare build directory: %w", err)
	}
	defer e.builds.Cleanup(dir)

	commitSHA, err := e.clone(ctx, req, report, dir)
	if err != nil {
		return DeployResult{}, err
	}

	tag := imageTag(req.Project, commitSHA)
	report("", fmt.Sprintf("Resolved commit %s", commitSHA))
	report(domain.StatusBuilding, fmt.Sprintf("Building image %s", tag))

	if err := e.buildImage(ctx, dir, tag, report); err != nil {
		return DeployResult{}, err
	}

	report(domain.StatusDeploying, fmt.Sprintf("Starting candidate container %s", containerName(req.Deployment.ID)))
	result, err := e.startContainer(ctx, req, tag, report)
	if err != nil {
		return DeployResult{}, err
	}

	// From here on the candidate exists, so any failure must clean it up.
	fail := func(cause error) (DeployResult, error) {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := e.Stop(cleanupCtx, req); stopErr != nil {
			report("", fmt.Sprintf("Warning: could not remove candidate container: %v", stopErr))
		}
		return DeployResult{}, cause
	}

	report(domain.StatusHealthCheck, fmt.Sprintf("Waiting for %s%s", loopbackURL(result.Port), req.Project.HealthCheckPath))
	if err := e.waitHealthy(ctx, req.Project, result.Port); err != nil {
		return fail(err)
	}

	report("", fmt.Sprintf("Health check passed on port %d", result.Port))
	return result, nil
}

func (e *DockerExecutor) clone(ctx context.Context, req DeployRequest, report Report, dir string) (string, error) {
	cloneDir := filepath.Join(dir, "repo")
	// --depth 1 keeps clones fast; --single-branch avoids fetching every ref.
	args := []string{"clone", "--depth", "1", "--single-branch", "--branch", req.Project.Branch}
	args = append(args, req.Project.RepoURL, cloneDir)

	emit := func(stream string, line string) { emitReport(report, stream, line) }
	if err := e.runner.Run(ctx, Command{Dir: dir, Binary: e.config.GitBinary, Args: args}, emit); err != nil {
		return "", fmt.Errorf("clone repository: %w", err)
	}

	output, err := e.runner.Output(ctx, Command{
		Dir:    cloneDir,
		Binary: e.config.GitBinary,
		Args:   []string{"rev-parse", "HEAD"},
	})
	if err != nil {
		return "", fmt.Errorf("resolve commit sha: %w", err)
	}
	commitSHA := strings.TrimSpace(output)
	if commitSHA == "" {
		return "", errors.New("resolve commit sha: empty result")
	}
	return commitSHA, nil
}

func (e *DockerExecutor) buildImage(ctx context.Context, dir string, tag string, report Report) error {
	buildArgs := []string{"build", "--tag", tag}
	buildArgs = append(buildArgs, e.config.buildResourceArgs()...)
	buildArgs = append(buildArgs, ".")
	emit := func(stream string, line string) { emitReport(report, stream, line) }
	return e.runner.Run(ctx, Command{Dir: filepath.Join(dir, "repo"), Binary: e.config.DockerBinary, Args: buildArgs}, emit)
}

// startContainer runs the image detached on the platform network and resolves
// the loopback port Docker assigned it.
//
// Publishing on 127.0.0.1 with an ephemeral port rather than a fixed one is what
// lets a candidate and the running previous version coexist during a deploy,
// which is the whole basis of safe rollback.
func (e *DockerExecutor) startContainer(ctx context.Context, req DeployRequest, tag string, report Report) (DeployResult, error) {
	name := containerName(req.Deployment.ID)
	port := e.config.ContainerPort

	args := []string{
		"run", "--detach", "--rm",
		"--name", name,
		"--network", e.config.Network,
		"--restart", "unless-stopped",
		"--publish", fmt.Sprintf("127.0.0.1::%d", port),
	}
	args = append(args, e.config.runResourceArgs()...)
	args = append(args, envArgs(req.EnvVars)...)
	args = append(args, tag)

	emit := func(stream string, line string) { emitReport(report, stream, line) }
	if err := e.runner.Run(ctx, Command{Binary: e.config.DockerBinary, Args: args}, emit); err != nil {
		return DeployResult{}, fmt.Errorf("start container: %w", err)
	}

	output, err := e.runner.Output(ctx, Command{
		Binary: e.config.DockerBinary,
		Args:   []string{"port", name, fmt.Sprintf("%d/tcp", port)},
	})
	if err != nil {
		return DeployResult{}, fmt.Errorf("resolve published port: %w", err)
	}
	published, err := parseDockerPort(output)
	if err != nil {
		return DeployResult{}, err
	}
	report("", fmt.Sprintf("Container %s listening on 127.0.0.1:%d", name, published))
	return DeployResult{ImageTag: tag, Container: name, Port: published}, nil
}

func (e *DockerExecutor) waitHealthy(ctx context.Context, project domain.Project, port int) error {
	if e.config.StartupGrace > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(e.config.StartupGrace):
		}
	}
	checker := e.health
	if checker == nil {
		checker = NewHTTPHealthChecker()
	}
	if err := checker.Wait(ctx, loopbackURL(port), project.HealthCheckPath, e.config.HealthTimeout); err != nil {
		// Wrap so the deployment log names what was probed, whatever the
		// HealthChecker implementation reports.
		return fmt.Errorf("health check on port %d (%s) failed: %w", port, project.HealthCheckPath, err)
	}
	return nil
}

// Stop removes the container for a deployment. Images are deliberately left in
// place: rollback re-runs them, and deleting them would make rollback slower
// and depend on a rebuild.
func (e *DockerExecutor) Stop(ctx context.Context, req DeployRequest) error {
	name := containerName(req.Deployment.ID)
	if err := e.runner.Run(ctx, Command{Binary: e.config.DockerBinary, Args: []string{"rm", "--force", name}}, nil); err != nil {
		// A missing container is success from the caller's point of view.
		if strings.Contains(err.Error(), "No such container") {
			return nil
		}
		return fmt.Errorf("remove container %s: %w", name, err)
	}
	return nil
}

// Rollback restarts the previously successful image as a fresh container so the
// route can be pointed back at it.
func (e *DockerExecutor) Rollback(ctx context.Context, req RollbackRequest, report Report) (RollbackResult, error) {
	report = normalizeReport(report)
	if req.Target.ImageTag == "" {
		return RollbackResult{}, fmt.Errorf("deployment %s has no image to roll back to", req.Target.ID)
	}
	name := containerName(req.Rollback.ID)
	port := e.config.ContainerPort

	report(domain.StatusDeploying, fmt.Sprintf("Restarting previous image %s", req.Target.ImageTag))

	args := []string{
		"run", "--detach", "--rm",
		"--name", name,
		"--network", e.config.Network,
		"--restart", "unless-stopped",
		"--publish", fmt.Sprintf("127.0.0.1::%d", port),
	}
	args = append(args, e.config.runResourceArgs()...)
	args = append(args, envArgs(req.EnvVars)...)
	args = append(args, req.Target.ImageTag)

	emit := func(stream string, line string) { emitReport(report, stream, line) }
	if err := e.runner.Run(ctx, Command{Binary: e.config.DockerBinary, Args: args}, emit); err != nil {
		return RollbackResult{}, fmt.Errorf("start rollback container: %w", err)
	}

	output, err := e.runner.Output(ctx, Command{
		Binary: e.config.DockerBinary,
		Args:   []string{"port", name, fmt.Sprintf("%d/tcp", port)},
	})
	if err != nil {
		return RollbackResult{}, fmt.Errorf("resolve published port: %w", err)
	}
	published, err := parseDockerPort(output)
	if err != nil {
		return RollbackResult{}, err
	}

	report(domain.StatusHealthCheck, fmt.Sprintf("Waiting for rollback to pass health check on port %d", published))
	if err := e.waitHealthy(ctx, req.Project, published); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = e.runner.Run(cleanupCtx, Command{Binary: e.config.DockerBinary, Args: []string{"rm", "--force", name}}, nil)
		return RollbackResult{}, err
	}

	return RollbackResult{Container: name, Port: published}, nil
}

// Prepare creates an isolated build directory for a deployment.
func (b BuildDir) Prepare(deploymentID string) (string, error) {
	root := BuildRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(root, "build-"+deploymentID+"-")
	if err != nil {
		return "", err
	}
	return dir, nil
}

// Cleanup removes a build directory. It is best effort: leaving a stray
// directory behind must never fail a deployment that otherwise succeeded.
func (b BuildDir) Cleanup(dir string) {
	if dir == "" {
		return
	}
	_ = os.RemoveAll(dir)
}

// Prune removes dangling images left behind by failed builds.
func (p ImagePruner) Prune(ctx context.Context, runner CommandRunner, binary string) error {
	if err := runner.Run(ctx, Command{Binary: binary, Args: []string{"image", "prune", "--force", "--filter", "until=24h"}}, nil); err != nil {
		return fmt.Errorf("prune images: %w", err)
	}
	return nil
}

// BuildRoot is where clones happen. Overridable for tests.
var BuildRoot = func() string { return filepath.Join(".data", "builds") }

func (c DockerConfig) buildResourceArgs() []string { return nil }

// runResourceArgs are the limits applied to `docker run`. They differ from the
// build-time limits because memory and CPU limits on a build would cap the
// builder itself rather than the resulting application.
func (c DockerConfig) runResourceArgs() []string {
	var args []string
	if c.MemoryLimit > 0 {
		args = append(args, "--memory", fmt.Sprintf("%d", c.MemoryLimit))
	}
	if c.CPUPercent > 0 {
		args = append(args, "--cpu-quota", fmt.Sprintf("%d", c.CPUPercent*1000))
	}
	return args
}

func envArgs(vars []domain.EnvVar) []string {
	args := make([]string, 0, len(vars))
	for _, item := range vars {
		// Never pass an empty key: docker would treat it as a malformed flag.
		if strings.TrimSpace(item.Key) == "" {
			continue
		}
		args = append(args, "--env", item.Key+"="+item.Value)
	}
	return args
}

// parseDockerPort extracts the host port from `docker port` output, which looks
// like "127.0.0.1:32768" and may contain several lines when a port is published
// on both IPv4 and IPv6.
func parseDockerPort(output string) (int, error) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		index := strings.LastIndex(line, ":")
		if index < 0 {
			continue
		}
		port := 0
		for _, r := range line[index+1:] {
			if r < '0' || r > '9' {
				port = 0
				break
			}
			port = port*10 + int(r-'0')
		}
		if port > 0 && port <= 65535 {
			return port, nil
		}
	}
	return 0, fmt.Errorf("could not parse published port from docker output %q", strings.TrimSpace(output))
}

// emitReport forwards command output to the deployment log. Docker writes
// progress to stderr, so stderr lines are surfaced as stderr in the dashboard
// rather than being relabelled as system output.
func emitReport(report Report, stream string, line string) {
	if report == nil || strings.TrimSpace(line) == "" {
		return
	}
	report("", stream+": "+line)
}
