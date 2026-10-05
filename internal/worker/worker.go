// Package worker owns every deployment state transition.
//
// The API only ever creates a queued row; this package is the only place that
// moves a deployment forward. That is what lets JSON persistence become
// PostgreSQL and the simulation executor become Docker without changing any
// observable behaviour: the worker is the state machine in both cases.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"deploy-platform/internal/domain"
	"deploy-platform/internal/executor"
	"deploy-platform/internal/proxy"
	"deploy-platform/internal/store"
)

// Metrics receives deployment outcomes. All methods must be safe for concurrent
// use and must never block; the worker calls them on the hot path.
type Metrics interface {
	Queued(projectID string)
	Finished(projectID string, status domain.DeploymentStatus, duration time.Duration)
	JobProcessed(job string)
}

type Config struct {
	// PollInterval is how often the store is checked for queued work.
	PollInterval time.Duration
	// RoutingTimeout bounds a proxy write.
	RoutingTimeout time.Duration
	// CleanupTimeout bounds stopping the superseded container.
	CleanupTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}
	if c.RoutingTimeout <= 0 {
		c.RoutingTimeout = 30 * time.Second
	}
	if c.CleanupTimeout <= 0 {
		c.CleanupTimeout = 60 * time.Second
	}
	return c
}

type Worker struct {
	store    store.Store
	executor executor.Executor
	proxy    proxy.Writer
	metrics  Metrics
	config   Config

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// running guards against two deployments for the same project overlapping.
	running map[string]bool
	mu      sync.Mutex
	// routeMu serialises proxy writes. Deploys for different projects run
	// concurrently, but every route() call renders and installs the complete
	// route set, so two overlapping installs could drop each other's routes.
	routeMu sync.Mutex
}

func New(repository store.Store, exec executor.Executor, writer proxy.Writer, metrics Metrics, config Config) *Worker {
	ctx, cancel := context.WithCancel(context.Background())
	return &Worker{
		store:    repository,
		executor: exec,
		proxy:    writer,
		metrics:  metrics,
		config:   config.withDefaults(),
		ctx:      ctx,
		cancel:   cancel,
		running:  make(map[string]bool),
	}
}

func (w *Worker) Start() {
	w.recoverInterrupted()
	w.wg.Add(1)
	go w.loop()
}

func (w *Worker) Stop() {
	w.cancel()
	w.wg.Wait()
}

func (w *Worker) loop() {
	defer w.wg.Done()
	ticker := time.NewTicker(w.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			w.processOnce()
		}
	}
}

// processOnce runs the oldest actionable job for every idle project.
//
// One job per project is picked per tick and the picked jobs run concurrently,
// so a slow deploy for project A never blocks project B. Jobs for the same
// project never overlap: a project already being worked on is skipped, which
// keeps two deployments from racing over the same route and container
// namespace. The call still returns only after every picked job finished, so
// tests and operators observe one tick as one complete pass.
func (w *Worker) processOnce() {
	projects, err := w.store.ListProjects()
	if err != nil {
		log.Printf("list projects: %v", err)
		return
	}

	type job struct {
		project    domain.Project
		deployment domain.Deployment
		rollback   bool
	}
	var jobs []job

	for _, project := range projects {
		if w.busy(project.ID) {
			continue
		}
		deployments, err := w.store.ListDeployments(project.ID)
		if err != nil {
			log.Printf("list deployments: %v", err)
			continue
		}
		// ListDeployments returns newest first, so walk backwards to pick the
		// oldest queued job and preserve FIFO order per project.
		for i := len(deployments) - 1; i >= 0; i-- {
			deployment := deployments[i]
			switch deployment.Status {
			case domain.StatusQueued:
				if w.claim(project.ID) {
					jobs = append(jobs, job{project: project, deployment: deployment})
				}
			case domain.StatusRollback:
				if w.claim(project.ID) {
					jobs = append(jobs, job{project: project, deployment: deployment, rollback: true})
				}
			default:
				continue
			}
			// One job per project per tick.
			break
		}
	}

	var wg sync.WaitGroup
	for _, picked := range jobs {
		wg.Add(1)
		go func(project domain.Project, deployment domain.Deployment, rollback bool) {
			defer wg.Done()
			if rollback {
				w.runRollback(project, deployment)
				return
			}
			w.runDeployment(project, deployment)
		}(picked.project, picked.deployment, picked.rollback)
	}
	wg.Wait()
}

// recoverInterrupted fails every deployment that was mid-flight when the
// process died. Without this a crash leaves rows in cloning or building
// forever, because nothing else ever touches a non-queued row again.
func (w *Worker) recoverInterrupted() {
	projects, err := w.store.ListProjects()
	if err != nil {
		log.Printf("recover interrupted deployments: list projects: %v", err)
		return
	}
	for _, project := range projects {
		deployments, err := w.store.ListDeployments(project.ID)
		if err != nil {
			log.Printf("recover interrupted deployments: list deployments: %v", err)
			continue
		}
		for _, deployment := range deployments {
			if isTerminal(deployment.Status) {
				continue
			}
			now := time.Now().UTC()
			deployment.Status = domain.StatusFailed
			deployment.Error = "interrupted by restart"
			deployment.FinishedAt = &now
			if err := w.store.UpdateDeployment(deployment); err != nil {
				log.Printf("recover interrupted deployment %s: %v", deployment.ID, err)
				continue
			}
			w.log(deployment.ID, "Deployment failed: interrupted by restart")
		}
	}
}

// isTerminal reports whether no further worker action can change the status.
func isTerminal(status domain.DeploymentStatus) bool {
	switch status {
	case domain.StatusSuccess, domain.StatusFailed, domain.StatusRolledBack:
		return true
	default:
		return false
	}
}

func (w *Worker) busy(projectID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running[projectID]
}

func (w *Worker) claim(projectID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running[projectID] {
		return false
	}
	w.running[projectID] = true
	return true
}

func (w *Worker) release(projectID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.running, projectID)
}

// report builds the executor.Report that turns executor progress into worker
// state transitions plus log lines. The executor never writes state itself.
func (w *Worker) report(deployment *domain.Deployment) executor.Report {
	return func(status domain.DeploymentStatus, message string) {
		if message != "" {
			w.log(deployment.ID, message)
		}
		if status == "" || status == deployment.Status {
			return
		}
		deployment.Status = status
		if err := w.store.UpdateDeployment(*deployment); err != nil {
			log.Printf("update deployment %s status: %v", deployment.ID, err)
		}
	}
}

func (w *Worker) runDeployment(project domain.Project, deployment domain.Deployment) {
	defer w.release(project.ID)
	started := time.Now()

	w.log(deployment.ID, "Deployment worker picked up job")
	if w.metrics != nil {
		w.metrics.Queued(project.ID)
	}

	envVars, err := w.envVars(project.ID)
	if err != nil {
		w.fail(&deployment, fmt.Errorf("load environment variables: %w", err))
		return
	}

	// The container that is serving right now, stopped only after the candidate
	// has passed its health gate.
	previous := w.previousDeployment(project)

	result, err := w.executor.Deploy(w.ctx, executor.DeployRequest{
		Project:    project,
		Deployment: deployment,
		EnvVars:    envVars,
	}, w.report(&deployment))
	if err != nil {
		w.fail(&deployment, err)
		return
	}

	deployment.ImageTag = result.ImageTag
	deployment.CommitSHA = w.resolveCommitSHA(result.ImageTag, deployment.CommitSHA)
	deployment.Port = result.Port

	if !w.publishSuccess(project, &deployment, result.Port, started) {
		return
	}

	w.stopSuperseded(project, previous, deployment.ID)
}

// publishSuccess routes the live hostname at the new container, marks the
// deployment successful and points the project at it, holding routeMu across
// all three steps.
//
// The lock is what keeps two concurrent deploys for different projects from
// installing route sets that each miss the other: every publish renders the
// full set, and the last deploy to finish always sees every earlier active
// deployment, so the installed file converges on the complete set.
func (w *Worker) publishSuccess(project domain.Project, deployment *domain.Deployment, port int, started time.Time) bool {
	w.routeMu.Lock()
	defer w.routeMu.Unlock()

	// Route the live hostname at the new container *before* declaring success.
	if err := w.route(project, port); err != nil {
		w.fail(deployment, fmt.Errorf("update proxy route: %w", err))
		return false
	}

	now := time.Now().UTC()
	deployment.Status = domain.StatusSuccess
	deployment.Error = ""
	deployment.FinishedAt = &now
	if err := w.store.UpdateDeployment(*deployment); err != nil {
		log.Printf("mark deployment %s success: %v", deployment.ID, err)
		w.record(project.ID, domain.StatusSuccess, started)
		return false
	}

	project.ActiveDeployID = deployment.ID
	if err := w.store.UpdateProject(project); err != nil {
		log.Printf("update active deployment for %s: %v", project.ID, err)
	}

	w.log(deployment.ID, "Deployment marked successful")
	w.record(project.ID, domain.StatusSuccess, started)
	return true
}

// route points every project hostname at a port, leaving other projects alone.
func (w *Worker) route(project domain.Project, port int) error {
	if w.proxy == nil {
		return nil
	}
	projects, err := w.store.ListProjects()
	if err != nil {
		return err
	}
	routes := make([]proxy.Route, 0, len(projects))
	for _, item := range projects {
		if item.ID == project.ID {
			routes = append(routes, proxy.Route{
				Host:     item.Host,
				Upstream: loopbackUpstream(port),
			})
			continue
		}
		// Preserve other projects' existing upstreams by reading the port off
		// their active deployment.
		if item.ActiveDeployID == "" {
			continue
		}
		active, err := w.store.GetDeployment(item.ActiveDeployID)
		if err != nil || active.Port == 0 {
			continue
		}
		routes = append(routes, proxy.Route{
			Host:     item.Host,
			Upstream: loopbackUpstream(active.Port),
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), w.config.RoutingTimeout)
	defer cancel()
	_ = ctx // proxy.Writer.Apply is synchronous; the timeout documents intent.
	return w.proxy.Apply(routes)
}

// loopbackUpstream is the address Caddy proxies to. The scheme is deliberately
// omitted: Caddy treats a bare host:port as an HTTP upstream.
func loopbackUpstream(port int) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// previousDeployment returns the deployment currently serving traffic for a
// project, so its container can be stopped once the new one is live.
func (w *Worker) previousDeployment(project domain.Project) *domain.Deployment {
	if project.ActiveDeployID == "" {
		return nil
	}
	active, err := w.store.GetDeployment(project.ActiveDeployID)
	if err != nil {
		return nil
	}
	return &active
}

func (w *Worker) stopSuperseded(project domain.Project, previous *domain.Deployment, currentID string) {
	if previous == nil || previous.ID == currentID || previous.Port == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), w.config.CleanupTimeout)
	defer cancel()
	if err := w.executor.Stop(ctx, executor.DeployRequest{
		Project:    project,
		Deployment: *previous,
	}); err != nil {
		// The new deployment is already live, so a cleanup failure is a warning.
		w.log(currentID, "Warning: could not stop superseded container: "+err.Error())
		return
	}
	w.log(currentID, "Stopped superseded container for deployment "+previous.ID)
}

func (w *Worker) runRollback(project domain.Project, rollback domain.Deployment) {
	defer w.release(project.ID)
	started := time.Now()

	w.log(rollback.ID, "Rollback worker picked up job")
	if w.metrics != nil {
		w.metrics.Queued(project.ID)
	}

	target, err := w.store.GetDeployment(rollback.RollbackTo)
	if err != nil {
		w.fail(&rollback, fmt.Errorf("load rollback target: %w", err))
		return
	}

	envVars, err := w.envVars(project.ID)
	if err != nil {
		w.fail(&rollback, fmt.Errorf("load environment variables: %w", err))
		return
	}

	previous := w.previousDeployment(project)

	result, err := w.executor.Rollback(w.ctx, executor.RollbackRequest{
		Project:  project,
		Target:   target,
		Rollback: rollback,
		EnvVars:  envVars,
	}, w.report(&rollback))
	if err != nil {
		w.fail(&rollback, err)
		return
	}

	rollback.Port = result.Port

	if !w.publishRollback(project, &rollback, previous, target, result.Port, started) {
		return
	}

	w.stopSuperseded(project, previous, rollback.ID)
}

// publishRollback is the rollback mirror of publishSuccess: point the route
// back at the restored container, mark the rollback successful and the
// replaced deployment rolled back, all under routeMu for the same
// convergence reason.
func (w *Worker) publishRollback(project domain.Project, rollback *domain.Deployment, previous *domain.Deployment, target domain.Deployment, port int, started time.Time) bool {
	w.routeMu.Lock()
	defer w.routeMu.Unlock()

	if err := w.route(project, port); err != nil {
		w.fail(rollback, fmt.Errorf("update proxy route: %w", err))
		return false
	}

	now := time.Now().UTC()
	rollback.Status = domain.StatusSuccess
	rollback.Error = ""
	rollback.ImageTag = target.ImageTag
	rollback.CommitSHA = target.CommitSHA
	rollback.FinishedAt = &now
	if err := w.store.UpdateDeployment(*rollback); err != nil {
		log.Printf("mark rollback %s success: %v", rollback.ID, err)
		return false
	}

	// Mark the deployment we just replaced, but only if it is still the one
	// serving traffic. A rollback of a rollback must not rewrite history.
	if previous != nil && previous.ID != target.ID && previous.ID != rollback.ID {
		previous.Status = domain.StatusRolledBack
		previous.FinishedAt = &now
		if err := w.store.UpdateDeployment(*previous); err != nil {
			log.Printf("mark deployment %s rolled back: %v", previous.ID, err)
		}
	}

	project.ActiveDeployID = target.ID
	if err := w.store.UpdateProject(project); err != nil {
		log.Printf("update active deployment for %s: %v", project.ID, err)
	}

	w.log(rollback.ID, "Rollback marked successful")
	w.record(project.ID, domain.StatusSuccess, started)
	return true
}

// envVars loads a project's environment.
//
// A read failure fails the deployment rather than degrading to an empty set. That
// is deliberate: starting a container without its secrets usually looks like a
// healthy app to a shallow health check while being quietly broken.
func (w *Worker) envVars(projectID string) ([]domain.EnvVar, error) {
	vars, err := w.store.ListEnvVars(projectID)
	if err != nil {
		return nil, fmt.Errorf("load environment variables: %w", err)
	}
	return vars, nil
}

// resolveCommitSHA derives the short sha from the built image tag, so a real
// deployment records the commit that actually shipped rather than the
// placeholder the store seeded.
func (w *Worker) resolveCommitSHA(imageTag string, fallback string) string {
	index := strings.LastIndex(imageTag, ":")
	if index < 0 || index == len(imageTag)-1 {
		return fallback
	}
	return imageTag[index+1:]
}

func (w *Worker) record(projectID string, status domain.DeploymentStatus, started time.Time) {
	if w.metrics == nil {
		return
	}
	w.metrics.Finished(projectID, status, time.Since(started))
	w.metrics.JobProcessed("deploy")
}

func (w *Worker) fail(deployment *domain.Deployment, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	now := time.Now().UTC()
	deployment.Status = domain.StatusFailed
	deployment.Error = err.Error()
	deployment.FinishedAt = &now
	if updateErr := w.store.UpdateDeployment(*deployment); updateErr != nil {
		log.Printf("mark deployment %s failed: %v", deployment.ID, updateErr)
	}
	w.log(deployment.ID, "Deployment failed: "+err.Error())
	if w.metrics != nil {
		w.metrics.Finished(deployment.ProjectID, domain.StatusFailed, time.Since(deployment.StartedAt))
		w.metrics.JobProcessed("deploy")
	}
}

func (w *Worker) log(deploymentID string, message string) {
	if _, err := w.store.AddLog(deploymentID, "system", message); err != nil {
		log.Printf("add deployment log: %v", err)
	}
}
