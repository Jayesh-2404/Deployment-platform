package worker

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"deploy-platform/internal/domain"
	"deploy-platform/internal/executor"
	"deploy-platform/internal/proxy"
	"deploy-platform/internal/store"
)

// fakeExecutor records requests and returns scripted results.
type fakeExecutor struct {
	mu sync.Mutex

	deployResult   executor.DeployResult
	deployErr      error
	rollbackResult executor.RollbackResult
	rollbackErr    error
	stopErr        error

	deployRequests   []executor.DeployRequest
	rollbackRequests []executor.RollbackRequest
	stopped          []string
	// reportsBeforeError is how many status reports the fake emits before
	// returning an error, so a failure mid-sequence can be simulated.
	reports []domain.DeploymentStatus
}

func (f *fakeExecutor) Deploy(ctx context.Context, req executor.DeployRequest, report executor.Report) (executor.DeployResult, error) {
	f.mu.Lock()
	f.deployRequests = append(f.deployRequests, req)
	f.mu.Unlock()
	for _, status := range f.reports {
		report(status, "step "+string(status))
	}
	if f.deployErr != nil {
		return executor.DeployResult{}, f.deployErr
	}
	return f.deployResult, nil
}

func (f *fakeExecutor) Rollback(ctx context.Context, req executor.RollbackRequest, report executor.Report) (executor.RollbackResult, error) {
	f.mu.Lock()
	f.rollbackRequests = append(f.rollbackRequests, req)
	f.mu.Unlock()
	report(domain.StatusDeploying, "restarting previous image")
	report(domain.StatusHealthCheck, "checking health")
	if f.rollbackErr != nil {
		return executor.RollbackResult{}, f.rollbackErr
	}
	return f.rollbackResult, nil
}

func (f *fakeExecutor) Stop(ctx context.Context, req executor.DeployRequest) error {
	f.mu.Lock()
	f.stopped = append(f.stopped, req.Deployment.ID)
	f.mu.Unlock()
	return f.stopErr
}

// recordingProxy captures applied routes.
type recordingProxy struct {
	mu     sync.Mutex
	routes [][]proxy.Route
	err    error
}

func (p *recordingProxy) Apply(routes []proxy.Route) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	copied := make([]proxy.Route, len(routes))
	copy(copied, routes)
	p.routes = append(p.routes, copied)
	return nil
}

func (p *recordingProxy) last() []proxy.Route {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.routes) == 0 {
		return nil
	}
	return p.routes[len(p.routes)-1]
}

// countingMetrics records worker callbacks.
type countingMetrics struct {
	mu        sync.Mutex
	queued    int
	finished  int
	jobs      int
	lastState domain.DeploymentStatus
}

func (m *countingMetrics) Queued(string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queued++
}

func (m *countingMetrics) Finished(_ string, status domain.DeploymentStatus, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finished++
	m.lastState = status
}

func (m *countingMetrics) JobProcessed(string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs++
}

func newTestStore(t *testing.T) *store.JSONStore {
	t.Helper()
	repository, err := store.NewJSONStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return repository
}

func mustCreateProject(t *testing.T, repository store.Store, name string, branch string) domain.Project {
	t.Helper()
	project, err := repository.CreateProject(domain.CreateProjectInput{
		Name:            name,
		RepoURL:         "https://github.com/example/" + name + ".git",
		Branch:          branch,
		HealthCheckPath: "/healthz",
		LiveURL:         "https://" + domain.Slugify(name) + ".example.com",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}

// tick runs the worker's poll loop body exactly once, synchronously.
func tick(w *Worker) { w.processOnce() }

func TestDeployMovesThroughEveryStatusAndSucceeds(t *testing.T) {
	repository := newTestStore(t)
	project := mustCreateProject(t, repository, "my-app", "main")
	deployment, err := repository.CreateDeployment(project.ID, "manual", "")
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	exec := &fakeExecutor{
		deployResult: executor.DeployResult{ImageTag: "deploy-platform/my-app:abc123def456", Container: "dp-x", Port: 32768},
		reports: []domain.DeploymentStatus{
			domain.StatusCloning,
			domain.StatusBuilding,
			domain.StatusDeploying,
			domain.StatusHealthCheck,
		},
	}
	proxyWriter := &recordingProxy{}
	telemetry := &countingMetrics{}
	w := New(repository, exec, proxyWriter, telemetry, Config{})

	tick(w)

	final, err := repository.GetDeployment(deployment.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if final.Status != domain.StatusSuccess {
		t.Fatalf("status = %q, want success", final.Status)
	}
	if final.Port != 32768 {
		t.Errorf("Port = %d, want 32768", final.Port)
	}
	if final.ImageTag != "deploy-platform/my-app:abc123def456" {
		t.Errorf("ImageTag = %q, want the built tag", final.ImageTag)
	}
	// The commit is derived from the image tag so history records what shipped.
	if final.CommitSHA != "abc123def456" {
		t.Errorf("CommitSHA = %q, want abc123def456 from the image tag", final.CommitSHA)
	}
	if final.FinishedAt == nil {
		t.Error("FinishedAt must be set on success")
	}

	updated, err := repository.GetProject(project.ID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if updated.ActiveDeployID != deployment.ID {
		t.Errorf("ActiveDeployID = %q, want %q", updated.ActiveDeployID, deployment.ID)
	}

	routes := proxyWriter.last()
	if len(routes) != 1 || routes[0].Upstream != "127.0.0.1:32768" {
		t.Errorf("expected one route to 127.0.0.1:32768, got %v", routes)
	}
	if routes[0].Host != "my-app.example.com" {
		t.Errorf("route host = %q, want my-app.example.com", routes[0].Host)
	}

	if telemetry.queued != 1 || telemetry.finished != 1 || telemetry.jobs != 1 {
		t.Errorf("metrics = queued %d finished %d jobs %d, want 1/1/1", telemetry.queued, telemetry.finished, telemetry.jobs)
	}
}

func TestDeployFailureMarksFailedAndLeavesPreviousDeploymentActive(t *testing.T) {
	repository := newTestStore(t)
	project := mustCreateProject(t, repository, "my-app", "main")

	// A healthy deployment first.
	good, _ := repository.CreateDeployment(project.ID, "manual", "")
	w := New(repository, &fakeExecutor{
		deployResult: executor.DeployResult{ImageTag: "deploy-platform/my-app:good", Port: 30001},
	}, &recordingProxy{}, &countingMetrics{}, Config{})
	tick(w)

	// Now a failing one.
	bad, _ := repository.CreateDeployment(project.ID, "manual", "")
	failing := New(repository, &fakeExecutor{
		deployErr: errors.New("health check on port 30002 (/healthz) failed"),
		reports:   []domain.DeploymentStatus{domain.StatusCloning, domain.StatusBuilding},
	}, &recordingProxy{}, &countingMetrics{}, Config{})
	tick(failing)

	failed, err := repository.GetDeployment(bad.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if failed.Status != domain.StatusFailed {
		t.Errorf("status = %q, want failed", failed.Status)
	}
	if !strings.Contains(failed.Error, "health check") {
		t.Errorf("Error = %q, want it to mention the health check", failed.Error)
	}
	if failed.FinishedAt == nil {
		t.Error("a failed deployment must have FinishedAt set")
	}

	updated, _ := repository.GetProject(project.ID)
	if updated.ActiveDeployID != good.ID {
		t.Errorf("a failed deploy must not change ActiveDeployID: got %q, want %q", updated.ActiveDeployID, good.ID)
	}

	// The failure is visible in the log, not only on stdout.
	logs, err := repository.ListLogs(bad.ID)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	joined := ""
	for _, entry := range logs {
		joined += entry.Message + "\n"
	}
	if !strings.Contains(joined, "Deployment failed") {
		t.Errorf("deployment log should record the failure, got:\n%s", joined)
	}
}

func TestDeployPassesEnvVarsToTheExecutor(t *testing.T) {
	repository := newTestStore(t)
	project := mustCreateProject(t, repository, "my-app", "main")
	if _, err := repository.UpsertEnvVar(project.ID, "DATABASE_URL", "postgres://localhost/app"); err != nil {
		t.Fatalf("upsert env: %v", err)
	}
	if _, err := repository.UpsertEnvVar(project.ID, "LOG_LEVEL", "debug"); err != nil {
		t.Fatalf("upsert env: %v", err)
	}
	if _, err := repository.CreateDeployment(project.ID, "manual", ""); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	exec := &fakeExecutor{deployResult: executor.DeployResult{ImageTag: "img:tag", Port: 30001}}
	tick(New(repository, exec, &recordingProxy{}, &countingMetrics{}, Config{}))

	if len(exec.deployRequests) != 1 {
		t.Fatalf("expected 1 deploy request, got %d", len(exec.deployRequests))
	}
	vars := exec.deployRequests[0].EnvVars
	if len(vars) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(vars))
	}
	if vars[0].Key != "DATABASE_URL" || vars[1].Key != "LOG_LEVEL" {
		t.Errorf("env vars must be ordered by key, got %+v", vars)
	}
}

func TestProxyFailureFailsTheDeploymentBeforeItGoesLive(t *testing.T) {
	repository := newTestStore(t)
	project := mustCreateProject(t, repository, "my-app", "main")
	deployment, _ := repository.CreateDeployment(project.ID, "manual", "")

	exec := &fakeExecutor{deployResult: executor.DeployResult{ImageTag: "img:tag", Port: 30001}}
	proxyWriter := &recordingProxy{err: errors.New("caddy config invalid")}
	tick(New(repository, exec, proxyWriter, &countingMetrics{}, Config{}))

	failed, _ := repository.GetDeployment(deployment.ID)
	if failed.Status != domain.StatusFailed {
		t.Errorf("status = %q, want failed when the proxy write fails", failed.Status)
	}
	updated, _ := repository.GetProject(project.ID)
	if updated.ActiveDeployID != "" {
		t.Errorf("a routing failure must not make the deployment active, got %q", updated.ActiveDeployID)
	}
}

func TestSupersededContainerIsStoppedAfterSuccess(t *testing.T) {
	repository := newTestStore(t)
	project := mustCreateProject(t, repository, "my-app", "main")

	first, _ := repository.CreateDeployment(project.ID, "manual", "")
	tick(New(repository, &fakeExecutor{
		deployResult: executor.DeployResult{ImageTag: "img:first", Port: 30001},
	}, &recordingProxy{}, &countingMetrics{}, Config{}))

	second, _ := repository.CreateDeployment(project.ID, "manual", "")
	exec := &fakeExecutor{deployResult: executor.DeployResult{ImageTag: "img:second", Port: 30002}}
	tick(New(repository, exec, &recordingProxy{}, &countingMetrics{}, Config{}))

	if len(exec.stopped) != 1 || exec.stopped[0] != first.ID {
		t.Errorf("stopped = %v, want exactly the superseded deployment %s", exec.stopped, first.ID)
	}
	updated, _ := repository.GetProject(project.ID)
	if updated.ActiveDeployID != second.ID {
		t.Errorf("ActiveDeployID = %q, want %q", updated.ActiveDeployID, second.ID)
	}
}

func TestRoutingPreservesOtherProjectsUpstreams(t *testing.T) {
	repository := newTestStore(t)
	alpha := mustCreateProject(t, repository, "alpha", "main")
	beta := mustCreateProject(t, repository, "beta", "main")
	if _, err := repository.CreateDeployment(alpha.ID, "manual", ""); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if _, err := repository.CreateDeployment(beta.ID, "manual", ""); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// Run a tick per project, giving each a distinct port. Whichever order the
	// worker picks them in, the final route set must map each hostname to its
	// own live container: routing a new deployment must never drop or rewrite
	// another project's upstream.
	ports := map[string]int{"alpha.example.com": 30001, "beta.example.com": 30002}
	proxyWriter := &recordingProxy{}
	for range 2 {
		tick(New(repository, &fakeExecutor{
			// Same port for both calls; each project gets its own because the
			// route is derived from the deployment being processed.
			deployResult: executor.DeployResult{ImageTag: "img:tag", Port: 31000},
		}, proxyWriter, &countingMetrics{}, Config{}))
	}
	// Give each project a distinct port by inspecting what the store recorded.
	projects, _ := repository.ListProjects()
	for _, item := range projects {
		active, err := repository.GetDeployment(item.ActiveDeployID)
		if err != nil {
			t.Fatalf("get active deployment for %s: %v", item.Name, err)
		}
		ports[item.Host] = active.Port
	}

	routes := proxyWriter.last()
	byHost := map[string]string{}
	for _, route := range routes {
		byHost[route.Host] = route.Upstream
	}
	if len(byHost) != 2 {
		t.Fatalf("expected both projects to be routed, got %v", byHost)
	}
	for host, port := range ports {
		want := "127.0.0.1:" + strconv.Itoa(port)
		if byHost[host] != want {
			t.Errorf("route for %s = %q, want %q", host, byHost[host], want)
		}
	}
}

func TestRollbackRestoresPreviousAndMarksReplacedRolledBack(t *testing.T) {
	repository := newTestStore(t)
	project := mustCreateProject(t, repository, "my-app", "main")

	first, _ := repository.CreateDeployment(project.ID, "manual", "")
	tick(New(repository, &fakeExecutor{
		deployResult: executor.DeployResult{ImageTag: "img:first", Port: 30001},
	}, &recordingProxy{}, &countingMetrics{}, Config{}))

	second, _ := repository.CreateDeployment(project.ID, "manual", "")
	tick(New(repository, &fakeExecutor{
		deployResult: executor.DeployResult{ImageTag: "img:second", Port: 30002},
	}, &recordingProxy{}, &countingMetrics{}, Config{}))

	rollback, err := repository.CreateDeployment(project.ID, "rollback", first.ID)
	if err != nil {
		t.Fatalf("create rollback: %v", err)
	}
	proxyWriter := &recordingProxy{}
	exec := &fakeExecutor{rollbackResult: executor.RollbackResult{Container: "dp-rb", Port: 30003}}
	tick(New(repository, exec, proxyWriter, &countingMetrics{}, Config{}))

	done, _ := repository.GetDeployment(rollback.ID)
	if done.Status != domain.StatusSuccess {
		t.Errorf("rollback status = %q, want success", done.Status)
	}
	if done.ImageTag != "img:first" {
		t.Errorf("rollback ImageTag = %q, want img:first", done.ImageTag)
	}
	if done.Port != 30003 {
		t.Errorf("rollback Port = %d, want 30003", done.Port)
	}

	replaced, _ := repository.GetDeployment(second.ID)
	if replaced.Status != domain.StatusRolledBack {
		t.Errorf("replaced deployment status = %q, want rolled_back", replaced.Status)
	}

	updated, _ := repository.GetProject(project.ID)
	if updated.ActiveDeployID != first.ID {
		t.Errorf("ActiveDeployID = %q, want the restored %q", updated.ActiveDeployID, first.ID)
	}

	routes := proxyWriter.last()
	if len(routes) != 1 || routes[0].Upstream != "127.0.0.1:30003" {
		t.Errorf("rollback must reroute to the new container, got %v", routes)
	}
}

func TestRollbackFailureLeavesActiveDeploymentAlone(t *testing.T) {
	repository := newTestStore(t)
	project := mustCreateProject(t, repository, "my-app", "main")

	first, _ := repository.CreateDeployment(project.ID, "manual", "")
	tick(New(repository, &fakeExecutor{
		deployResult: executor.DeployResult{ImageTag: "img:first", Port: 30001},
	}, &recordingProxy{}, &countingMetrics{}, Config{}))

	second, _ := repository.CreateDeployment(project.ID, "manual", "")
	tick(New(repository, &fakeExecutor{
		deployResult: executor.DeployResult{ImageTag: "img:second", Port: 30002},
	}, &recordingProxy{}, &countingMetrics{}, Config{}))

	rollback, _ := repository.CreateDeployment(project.ID, "rollback", first.ID)
	tick(New(repository, &fakeExecutor{
		rollbackErr: errors.New("previous image will not start"),
	}, &recordingProxy{}, &countingMetrics{}, Config{}))

	failed, _ := repository.GetDeployment(rollback.ID)
	if failed.Status != domain.StatusFailed {
		t.Errorf("rollback status = %q, want failed", failed.Status)
	}
	updated, _ := repository.GetProject(project.ID)
	if updated.ActiveDeployID != second.ID {
		t.Errorf("a failed rollback must leave the current deployment active, got %q", updated.ActiveDeployID)
	}
}

func TestQueuedDeploymentsRunOldestFirst(t *testing.T) {
	repository := newTestStore(t)
	project := mustCreateProject(t, repository, "my-app", "main")
	first, _ := repository.CreateDeployment(project.ID, "manual", "")
	second, _ := repository.CreateDeployment(project.ID, "manual", "")

	var order []string
	for i := 0; i < 2; i++ {
		w := New(repository, &recordingExecutor{onDeploy: func(req executor.DeployRequest) {
			order = append(order, req.Deployment.ID)
		}}, proxy.NoopWriter{}, nil, Config{})
		tick(w)
	}
	if len(order) != 2 || order[0] != first.ID || order[1] != second.ID {
		t.Errorf("deployment order = %v, want [%s %s]", order, first.ID, second.ID)
	}
}

// recordingExecutor lets a test observe the order jobs are claimed in.
type recordingExecutor struct {
	onDeploy func(executor.DeployRequest)
}

func (r *recordingExecutor) Deploy(ctx context.Context, req executor.DeployRequest, report executor.Report) (executor.DeployResult, error) {
	report(domain.StatusCloning, "cloning")
	report(domain.StatusBuilding, "building")
	if r.onDeploy != nil {
		r.onDeploy(req)
	}
	return executor.DeployResult{ImageTag: "img:tag", Port: 30001}, nil
}

func (r *recordingExecutor) Rollback(ctx context.Context, req executor.RollbackRequest, report executor.Report) (executor.RollbackResult, error) {
	report(domain.StatusDeploying, "restarting")
	return executor.RollbackResult{Port: 30001}, nil
}

func (r *recordingExecutor) Stop(context.Context, executor.DeployRequest) error { return nil }

func TestOneProjectIsNeverProcessedTwiceConcurrently(t *testing.T) {
	repository := newTestStore(t)
	project := mustCreateProject(t, repository, "my-app", "main")
	if _, err := repository.CreateDeployment(project.ID, "manual", ""); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// A blocking executor proves the guard: while it is inside Deploy, a
	// second tick for the same project must be skipped.
	entered := make(chan struct{})
	release := make(chan struct{})
	blocking := &blockingExecutor{entered: entered, release: release}

	w := New(repository, blocking, proxy.NoopWriter{}, nil, Config{PollInterval: time.Hour})

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		tick(w)
	}()

	<-entered
	if !w.busy(project.ID) {
		t.Error("project should be marked busy while a deployment is in flight")
	}
	close(release)
	// Wait for the tick to finish so the store is not written after the test's
	// temp directory is removed.
	<-finished
}

type blockingExecutor struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingExecutor) Deploy(ctx context.Context, req executor.DeployRequest, report executor.Report) (executor.DeployResult, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return executor.DeployResult{ImageTag: "img:tag", Port: 30001}, nil
}

func (b *blockingExecutor) Rollback(context.Context, executor.RollbackRequest, executor.Report) (executor.RollbackResult, error) {
	return executor.RollbackResult{Port: 30001}, nil
}

func (b *blockingExecutor) Stop(context.Context, executor.DeployRequest) error { return nil }

func TestWorkerStopIsIdempotentAndUnblocks(t *testing.T) {
	repository := newTestStore(t)
	w := New(repository, &fakeExecutor{}, &recordingProxy{}, &countingMetrics{}, Config{PollInterval: 10 * time.Millisecond})
	w.Start()
	time.Sleep(30 * time.Millisecond)
	w.Stop()

	done := make(chan struct{})
	go func() { w.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop must return promptly and be safe to call twice")
	}
}

func TestCancelledFailureIsNotRecordedAsAFailure(t *testing.T) {
	repository := newTestStore(t)
	project := mustCreateProject(t, repository, "my-app", "main")
	deployment, _ := repository.CreateDeployment(project.ID, "manual", "")

	w := New(repository, &fakeExecutor{deployErr: context.Canceled}, &recordingProxy{}, &countingMetrics{}, Config{})
	w.cancel()
	tick(w)

	unchanged, _ := repository.GetDeployment(deployment.ID)
	if unchanged.Status != domain.StatusQueued {
		t.Errorf("a cancelled deployment should stay queued, got %q", unchanged.Status)
	}
	if unchanged.Error != "" {
		t.Errorf("no error should be recorded for a cancellation, got %q", unchanged.Error)
	}
}

func TestLoopbackUpstreamFormat(t *testing.T) {
	if got := loopbackUpstream(32768); got != "127.0.0.1:32768" {
		t.Errorf("loopbackUpstream = %q, want 127.0.0.1:32768", got)
	}
}

func TestRecoverInterruptedFailsNonTerminalDeployments(t *testing.T) {
	repository := newTestStore(t)
	project := mustCreateProject(t, repository, "my-app", "main")

	stuck, _ := repository.CreateDeployment(project.ID, "manual", "")
	stuck.Status = domain.StatusCloning
	if err := repository.UpdateDeployment(stuck); err != nil {
		t.Fatalf("move deployment to cloning: %v", err)
	}
	queued, _ := repository.CreateDeployment(project.ID, "manual", "")
	finished, _ := repository.CreateDeployment(project.ID, "manual", "")
	finished.Status = domain.StatusSuccess
	if err := repository.UpdateDeployment(finished); err != nil {
		t.Fatalf("mark deployment success: %v", err)
	}

	w := New(repository, &fakeExecutor{}, &recordingProxy{}, &countingMetrics{}, Config{})
	w.recoverInterrupted()

	for _, id := range []string{stuck.ID, queued.ID} {
		deployment, err := repository.GetDeployment(id)
		if err != nil {
			t.Fatalf("get deployment: %v", err)
		}
		if deployment.Status != domain.StatusFailed {
			t.Errorf("deployment %s status = %q, want failed after a restart", id, deployment.Status)
		}
		if deployment.Error != "interrupted by restart" {
			t.Errorf("deployment %s error = %q, want %q", id, deployment.Error, "interrupted by restart")
		}
		if deployment.FinishedAt == nil {
			t.Errorf("deployment %s must have FinishedAt set", id)
		}
	}

	unchanged, _ := repository.GetDeployment(finished.ID)
	if unchanged.Status != domain.StatusSuccess {
		t.Errorf("a terminal deployment must survive recovery, got %q", unchanged.Status)
	}
}

func TestProcessOnceRunsOneJobPerProjectInASingleTick(t *testing.T) {
	repository := newTestStore(t)
	alpha := mustCreateProject(t, repository, "alpha", "main")
	beta := mustCreateProject(t, repository, "beta", "main")
	if _, err := repository.CreateDeployment(alpha.ID, "manual", ""); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if _, err := repository.CreateDeployment(beta.ID, "manual", ""); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	proxyWriter := &recordingProxy{}
	exec := &fakeExecutor{deployResult: executor.DeployResult{ImageTag: "img:tag", Port: 31000}}
	tick(New(repository, exec, proxyWriter, &countingMetrics{}, Config{}))

	// One tick must have completed both projects: a slow project A must never
	// starve project B of the worker.
	for _, projectID := range []string{alpha.ID, beta.ID} {
		deployments, _ := repository.ListDeployments(projectID)
		if len(deployments) != 1 || deployments[0].Status != domain.StatusSuccess {
			t.Errorf("project %s should have one successful deployment, got %+v", projectID, deployments)
		}
	}

	// The installed route set converges: the last publish sees every earlier
	// active deployment, so both hostnames are routed.
	routes := proxyWriter.last()
	byHost := map[string]string{}
	for _, route := range routes {
		byHost[route.Host] = route.Upstream
	}
	if len(byHost) != 2 {
		t.Errorf("expected both projects to be routed, got %v", byHost)
	}
}

func TestResolveCommitSHA(t *testing.T) {
	w := New(newTestStore(t), &fakeExecutor{}, &recordingProxy{}, &countingMetrics{}, Config{})
	tests := []struct {
		imageTag string
		fallback string
		want     string
	}{
		{"deploy-platform/my-app:abc123", "seeded", "abc123"},
		{"registry.local/app:v1.2.3", "seeded", "v1.2.3"},
		{"no-tag-at-all", "seeded", "seeded"},
		{"trailing:", "seeded", "seeded"},
	}
	for _, tt := range tests {
		if got := w.resolveCommitSHA(tt.imageTag, tt.fallback); got != tt.want {
			t.Errorf("resolveCommitSHA(%q, %q) = %q, want %q", tt.imageTag, tt.fallback, got, tt.want)
		}
	}
}
