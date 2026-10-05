package executor

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"deploy-platform/internal/domain"
)

// fakeRunner records every command and returns scripted output per binary.
type fakeRunner struct {
	commands []Command
	// output maps a matcher to the output returned by Output for it.
	output map[string]string
	// failOn makes Run return an error for commands matching this substring.
	failOn string
	// failError is the message returned when failOn matches.
	failError string
	// emitted records every line passed to Run's emit callback.
	emitted []string
}

// signature includes the binary so matchers can target e.g. "docker port".
func (f *fakeRunner) signature(cmd Command) string {
	return cmd.Binary + " " + strings.Join(cmd.Args, " ")
}

func (f *fakeRunner) Output(ctx context.Context, cmd Command) (string, error) {
	f.commands = append(f.commands, cmd)
	signature := f.signature(cmd)
	for matcher, value := range f.output {
		if strings.Contains(signature, matcher) {
			return value, nil
		}
	}
	return "", nil
}

func (f *fakeRunner) Run(ctx context.Context, cmd Command, emit func(string, string)) error {
	f.commands = append(f.commands, cmd)
	signature := f.signature(cmd)
	if f.failOn != "" && strings.Contains(signature, f.failOn) {
		message := f.failError
		if message == "" {
			message = "simulated command failure"
		}
		return errors.New(message)
	}
	if emit != nil {
		if strings.Contains(signature, "build") {
			emit("stdout", "Step 1/4 : FROM node:22-alpine")
			emit("stderr", "npm warn deprecated package")
			f.emitted = append(f.emitted, "npm warn deprecated package")
		}
	}
	return nil
}

func (f *fakeRunner) ran(match string) bool {
	for _, cmd := range f.commands {
		if strings.Contains(f.signature(cmd), match) {
			return true
		}
	}
	return false
}

// stubHealth stands in for the HTTP health check so no port must be listening.
type stubHealth struct {
	err        error
	calls      int
	baseURL    string
	path       string
	allowAfter int
}

func (s *stubHealth) Wait(ctx context.Context, baseURL string, path string, timeout time.Duration) error {
	s.calls++
	s.baseURL = baseURL
	s.path = path
	if s.allowAfter > 0 && s.calls < s.allowAfter {
		return errors.New("still starting")
	}
	return s.err
}

func testProject() domain.Project {
	return domain.Project{
		ID:              "proj_abc123",
		Name:            "My App",
		RepoURL:         "https://github.com/example/my-app.git",
		Branch:          "main",
		HealthCheckPath: "/healthz",
		Host:            "my-app.example.com",
	}
}

func testDeployment() domain.Deployment {
	return domain.Deployment{
		ID:        "dep_abc123",
		ProjectID: "proj_abc123",
		StartedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	}
}

// useTempBuildRoot points BuildDir.Prepare at a per-test temp directory.
func useTempBuildRoot(t *testing.T) {
	t.Helper()
	useTempBuildRootDir(t)
}

// useTempBuildRootDir is the same, but returns the directory so a test can
// assert on what is left behind.
func useTempBuildRootDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	original := BuildRoot
	BuildRoot = func() string { return dir }
	t.Cleanup(func() { BuildRoot = original })
	return dir
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestDeployHappyPathReportsEveryStatus(t *testing.T) {
	useTempBuildRoot(t)
	runner := &fakeRunner{output: map[string]string{
		"rev-parse":   "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b\n",
		"docker port": "127.0.0.1:32768\n",
	}}
	health := &stubHealth{}
	exec := NewDockerExecutor(runner, health, DockerConfig{StartupGrace: time.Millisecond})

	var statuses []domain.DeploymentStatus
	var messages []string
	result, err := exec.Deploy(context.Background(), DeployRequest{
		Project:    testProject(),
		Deployment: testDeployment(),
	}, func(status domain.DeploymentStatus, message string) {
		if status != "" {
			statuses = append(statuses, status)
		}
		messages = append(messages, message)
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	// The status sequence is the contract the dashboard and the docs describe.
	want := []domain.DeploymentStatus{
		domain.StatusCloning,
		domain.StatusBuilding,
		domain.StatusDeploying,
		domain.StatusHealthCheck,
	}
	if len(statuses) != len(want) {
		t.Fatalf("status sequence = %v, want %v", statuses, want)
	}
	for i := range want {
		if statuses[i] != want[i] {
			t.Errorf("status[%d] = %q, want %q", i, statuses[i], want[i])
		}
	}

	if result.Port != 32768 {
		t.Errorf("Port = %d, want 32768", result.Port)
	}
	if result.ImageTag != "deploy-platform/my-app:1a2b3c4d5e6f" {
		t.Errorf("ImageTag = %q, want deploy-platform/my-app:1a2b3c4d5e6f", result.ImageTag)
	}
	if result.Container != "dp-dep-abc123" {
		t.Errorf("Container = %q, want dp-dep-abc123", result.Container)
	}
	if health.path != "/healthz" {
		t.Errorf("health path = %q, want /healthz", health.path)
	}
	if health.baseURL != "http://127.0.0.1:32768" {
		t.Errorf("health baseURL = %q, want http://127.0.0.1:32768", health.baseURL)
	}
}

func TestDeployClonesSelectedBranchAtDepthOne(t *testing.T) {
	useTempBuildRoot(t)
	runner := &fakeRunner{output: map[string]string{
		"rev-parse":   "abc123\n",
		"docker port": "127.0.0.1:30001\n",
	}}
	exec := NewDockerExecutor(runner, &stubHealth{}, DockerConfig{StartupGrace: time.Millisecond})

	if _, err := exec.Deploy(context.Background(), DeployRequest{
		Project:    testProject(),
		Deployment: testDeployment(),
	}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	if !runner.ran("clone --depth 1 --single-branch --branch main") {
		t.Errorf("expected a shallow clone of the main branch, got %v", runner.commands)
	}
	if !runner.ran("https://github.com/example/my-app.git") {
		t.Error("expected the configured repo URL in the clone args")
	}
}

func TestDeployInjectsEnvVarsAndResourceLimits(t *testing.T) {
	useTempBuildRoot(t)
	runner := &fakeRunner{output: map[string]string{
		"rev-parse":   "abc123\n",
		"docker port": "127.0.0.1:30002\n",
	}}
	exec := NewDockerExecutor(runner, &stubHealth{}, DockerConfig{
		StartupGrace: time.Millisecond,
		MemoryLimit:  512 * 1024 * 1024,
		CPUPercent:   50,
	})

	if _, err := exec.Deploy(context.Background(), DeployRequest{
		Project:    testProject(),
		Deployment: testDeployment(),
		EnvVars: []domain.EnvVar{
			{Key: "DATABASE_URL", Value: "postgres://localhost/app"},
			{Key: "LOG_LEVEL", Value: "debug"},
		},
	}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	joined := ""
	for _, cmd := range runner.commands {
		if len(cmd.Args) > 0 && cmd.Args[0] == "run" {
			joined = runner.signature(cmd)
		}
	}
	for _, want := range []string{
		"--env DATABASE_URL=postgres://localhost/app",
		"--env LOG_LEVEL=debug",
		"--memory 536870912",
		"--cpu-quota 50000",
		"--network deploy-platform",
		"127.0.0.1::3000",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker run args missing %q\ngot: %s", want, joined)
		}
	}
}

func TestDeployFailureStopsCandidateAndKeepsPreviousContainer(t *testing.T) {
	useTempBuildRoot(t)
	runner := &fakeRunner{output: map[string]string{
		"rev-parse":   "abc123\n",
		"docker port": "127.0.0.1:30003\n",
	}}
	health := &stubHealth{err: errors.New("health endpoint returned status 503")}
	exec := NewDockerExecutor(runner, health, DockerConfig{StartupGrace: time.Millisecond})

	result, err := exec.Deploy(context.Background(), DeployRequest{
		Project:    testProject(),
		Deployment: testDeployment(),
	}, nil)
	if err == nil {
		t.Fatal("expected a health check failure, got nil")
	}
	if result.Port != 0 || result.Container != "" {
		t.Errorf("failed deploy must not report a usable result, got %+v", result)
	}
	// The candidate must be torn down; the previous container is never touched
	// because its name is derived from a different deployment ID.
	if !runner.ran("rm --force dp-dep-abc123") {
		t.Error("expected the candidate container to be removed after a failed health check")
	}
	if !strings.Contains(err.Error(), "health check on port 30003 (/healthz) failed") {
		t.Errorf("error should name the probed port and path, got %v", err)
	}
}

func TestDeployBuildFailureDoesNotStartContainer(t *testing.T) {
	useTempBuildRoot(t)
	runner := &fakeRunner{failOn: "build", output: map[string]string{"rev-parse": "abc123\n"}}
	exec := NewDockerExecutor(runner, &stubHealth{}, DockerConfig{StartupGrace: time.Millisecond})

	_, err := exec.Deploy(context.Background(), DeployRequest{
		Project:    testProject(),
		Deployment: testDeployment(),
	}, nil)
	if err == nil {
		t.Fatal("expected a build failure, got nil")
	}
	if runner.ran("run --detach") {
		t.Error("a failed build must not start a container")
	}
	if !strings.Contains(err.Error(), "clone repository") && !strings.Contains(err.Error(), "build") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDeployCleansUpBuildDirectory(t *testing.T) {
	root := useTempBuildRootDir(t)
	runner := &fakeRunner{output: map[string]string{
		"rev-parse":   "abc123\n",
		"docker port": "127.0.0.1:30004\n",
	}}
	exec := NewDockerExecutor(runner, &stubHealth{}, DockerConfig{StartupGrace: time.Millisecond})

	if _, err := exec.Deploy(context.Background(), DeployRequest{
		Project:    testProject(),
		Deployment: testDeployment(),
	}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if entries := listDir(t, root); len(entries) != 0 {
		t.Errorf("build root should be empty after a deploy, found %v", entries)
	}
}

func TestDeployCancelledDuringHealthCheck(t *testing.T) {
	useTempBuildRoot(t)
	ctx, cancel := context.WithCancel(context.Background())
	runner := &fakeRunner{output: map[string]string{
		"rev-parse":   "abc123\n",
		"docker port": "127.0.0.1:30005\n",
	}}
	health := &stubHealth{err: context.Canceled}
	exec := NewDockerExecutor(runner, health, DockerConfig{StartupGrace: time.Millisecond})
	cancel()

	if _, err := exec.Deploy(ctx, DeployRequest{
		Project:    testProject(),
		Deployment: testDeployment(),
	}, nil); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
	if !runner.ran("rm --force") {
		t.Error("a cancelled deploy must still clean up its candidate container")
	}
}

func TestRollbackRestartsPreviousImageAndHealthChecks(t *testing.T) {
	useTempBuildRoot(t)
	runner := &fakeRunner{output: map[string]string{
		"docker port": "127.0.0.1:30006\n",
	}}
	exec := NewDockerExecutor(runner, &stubHealth{}, DockerConfig{StartupGrace: time.Millisecond})

	var statuses []domain.DeploymentStatus
	result, err := exec.Rollback(context.Background(), RollbackRequest{
		Project: testProject(),
		Target: domain.Deployment{
			ID:       "dep_previous",
			ImageTag: "deploy-platform/my-app:aaaabbbbcccc",
		},
		Rollback: domain.Deployment{ID: "dep_rollback"},
	}, func(status domain.DeploymentStatus, message string) {
		if status != "" {
			statuses = append(statuses, status)
		}
	})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	want := []domain.DeploymentStatus{domain.StatusDeploying, domain.StatusHealthCheck}
	if len(statuses) != len(want) {
		t.Fatalf("statuses = %v, want %v", statuses, want)
	}
	if result.Port != 30006 || result.Container != "dp-dep-rollback" {
		t.Errorf("result = %+v, want port 30006 container dp-dep-rollback", result)
	}
	if !runner.ran("deploy-platform/my-app:aaaabbbbcccc") {
		t.Error("rollback must run the previous image tag")
	}
}

func TestRollbackWithoutImageFails(t *testing.T) {
	exec := NewDockerExecutor(&fakeRunner{}, &stubHealth{}, DockerConfig{})
	_, err := exec.Rollback(context.Background(), RollbackRequest{
		Project:  testProject(),
		Target:   domain.Deployment{ID: "dep_previous"},
		Rollback: domain.Deployment{ID: "dep_rollback"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "no image to roll back to") {
		t.Fatalf("expected a missing-image error, got %v", err)
	}
}

func TestRollbackHealthFailureRemovesContainer(t *testing.T) {
	runner := &fakeRunner{output: map[string]string{"docker port": "127.0.0.1:30007\n"}}
	exec := NewDockerExecutor(runner, &stubHealth{err: errors.New("unhealthy")}, DockerConfig{
		StartupGrace: time.Millisecond,
	})
	if _, err := exec.Rollback(context.Background(), RollbackRequest{
		Project:  testProject(),
		Target:   domain.Deployment{ID: "dep_prev", ImageTag: "img:tag"},
		Rollback: domain.Deployment{ID: "dep_rb"},
		EnvVars:  nil,
	}, nil); err == nil {
		t.Fatal("expected the rollback health check to fail")
	}
	if !runner.ran("rm --force dp-dep-rb") {
		t.Error("a failed rollback must remove its container")
	}
}

func TestStopRemovesContainer(t *testing.T) {
	runner := &fakeRunner{}
	exec := NewDockerExecutor(runner, &stubHealth{}, DockerConfig{})
	if err := exec.Stop(context.Background(), DeployRequest{Deployment: testDeployment()}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !runner.ran("rm --force dp-dep-abc123") {
		t.Error("Stop must attempt to remove the deployment container")
	}
	// Images must survive: rollback re-runs them.
	if runner.ran("image rm") || runner.ran("rmi") {
		t.Error("Stop must not remove images, because rollback needs them")
	}
}

func TestStopTreatsMissingContainerAsSuccess(t *testing.T) {
	runner := &fakeRunner{
		failOn:    "rm --force",
		failError: `Error response from daemon: No such container: dp-dep-abc123`,
	}
	exec := NewDockerExecutor(runner, &stubHealth{}, DockerConfig{})
	if err := exec.Stop(context.Background(), DeployRequest{Deployment: testDeployment()}); err != nil {
		t.Fatalf("removing an already-absent container must succeed, got %v", err)
	}
}

func TestStopReportsRealDockerFailures(t *testing.T) {
	runner := &fakeRunner{failOn: "rm --force", failError: "permission denied while trying to connect to the Docker daemon"}
	exec := NewDockerExecutor(runner, &stubHealth{}, DockerConfig{})
	err := exec.Stop(context.Background(), DeployRequest{Deployment: testDeployment()})
	if err == nil {
		t.Fatal("a genuine docker failure must be reported, not swallowed")
	}
	if !strings.Contains(err.Error(), "dp-dep-abc123") {
		t.Errorf("error should name the container, got %v", err)
	}
}

func TestParseDockerPort(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    int
		wantErr bool
	}{
		{"single ipv4", "127.0.0.1:32768\n", 32768, false},
		{"ipv4 and ipv6", "127.0.0.1:32768\n[::1]:32768\n", 32768, false},
		{"leading whitespace", "  0.0.0.0:49152  \n", 49152, false},
		{"empty", "", 0, true},
		{"no port", "notaport\n", 0, true},
		{"non numeric port", "127.0.0.1:abc\n", 0, true},
		{"port out of range", "127.0.0.1:99999\n", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDockerPort(tt.output)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tt.output)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDockerPort(%q): %v", tt.output, err)
			}
			if got != tt.want {
				t.Errorf("parseDockerPort(%q) = %d, want %d", tt.output, got, tt.want)
			}
		})
	}
}

func TestContainerNameAndImageTag(t *testing.T) {
	if got := containerName("dep_abc123"); got != "dp-dep-abc123" {
		t.Errorf("containerName = %q, want dp-dep-abc123", got)
	}
	if got := containerName("weird/id with spaces"); got != "dp-weirdidwithspaces" {
		t.Errorf("containerName must strip invalid characters, got %q", got)
	}
	project := domain.Project{Name: "My App"}
	if got := imageTag(project, "ABCDEF0123456789"); got != "deploy-platform/my-app:abcdef012345" {
		t.Errorf("imageTag = %q, want deploy-platform/my-app:abcdef012345", got)
	}
	if got := imageTag(project, "  "); got != "deploy-platform/my-app:latest" {
		t.Errorf("imageTag with empty sha = %q, want ...:latest", got)
	}
}

func TestCommandStringQuotesArgsAndHidesEnv(t *testing.T) {
	cmd := Command{Binary: "docker", Args: []string{"run", "--env", "A=b", "my image"}, Env: []string{"SECRET=hunter2"}}
	got := cmd.String()
	if !strings.Contains(got, `"my image"`) {
		t.Errorf("args with spaces should be quoted, got %s", got)
	}
	if strings.Contains(got, "hunter2") {
		t.Errorf("Command.String must never include env values, got %s", got)
	}
}
