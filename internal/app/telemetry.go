package app

import (
	"time"

	"deploy-platform/internal/domain"
	"deploy-platform/internal/metrics"
	"deploy-platform/internal/worker"
)

// Telemetry adapts the metrics registry to the worker's and store's needs. It
// is the only place that knows metric names, so adding a metric does not scatter
// string literals through the worker.
type Telemetry struct {
	registry       *metrics.Registry
	deployQueued   *metrics.Counter
	deployFinished *metrics.Counter
	deployDuration *metrics.Histogram
	jobsProcessed  *metrics.Counter
	projectsActive *metrics.Gauge
}

func NewTelemetry(registry *metrics.Registry) *Telemetry {
	return &Telemetry{
		registry: registry,
		deployQueued: registry.Counter(
			"deploy_platform_deployments_queued_total",
			"Deployments enqueued for the worker.",
			"project",
		),
		deployFinished: registry.Counter(
			"deploy_platform_deployments_finished_total",
			"Deployments that reached a terminal state.",
			"project", "status",
		),
		deployDuration: registry.Histogram(
			"deploy_platform_deployment_duration_seconds",
			"Wall-clock time from enqueue to terminal state.",
			[]float64{1, 5, 15, 30, 60, 120, 300, 600, 1800},
			"project",
		),
		jobsProcessed: registry.Counter(
			"deploy_platform_worker_jobs_total",
			"Jobs processed by the worker.",
			"job",
		),
		projectsActive: registry.Gauge(
			"deploy_platform_projects",
			"Projects known to the control plane.",
		),
	}
}

func (t *Telemetry) Queued(projectID string) {
	t.deployQueued.With(projectID).Inc()
}

func (t *Telemetry) Finished(projectID string, status domain.DeploymentStatus, duration time.Duration) {
	t.deployFinished.With(projectID, string(status)).Inc()
	t.deployDuration.With(projectID).Observe(duration.Seconds())
}

func (t *Telemetry) JobProcessed(job string) {
	t.jobsProcessed.With(job).Inc()
}

func (t *Telemetry) SetProjects(count int) {
	t.projectsActive.Set(float64(count))
}

// Telemetry must satisfy the worker's contract. This assertion keeps the two
// sides honest at compile time.
var _ worker.Metrics = (*Telemetry)(nil)
