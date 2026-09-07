# Deployment Platform

## What it is

Self-hosted deployment platform for a single VPS and a single developer. Input is a GitHub repository URL. Output is a running container behind a live URL.

It handles: repo clone, Docker image build, container start on an internal port, Caddy route update, health check gating, real-time deploy logs, deployment history, and rollback to the previous successful deployment.

Scope is intentionally narrow. No Kubernetes, no multi-region, no teams/billing, no serverless. One server, Docker containers, one reverse proxy.

Current code in this repo is V1: the local control-plane slice. The API, worker, executor boundary, and status machine are real. Persistence is a JSON file and the executor is simulated so Docker/Postgres are not required yet.

## Tech stack

Target stack:

- Backend: Go 1.24, stdlib `net/http`, single binary (API + worker in one process)
- Database: PostgreSQL — `projects`, `deployments`, `deployment_logs`, `environment_variables` (V1 uses `.data/state.json` behind the same `Store` interface)
- Runtime: Docker and Docker Compose, internal Docker network, per-project containers with CPU/memory limits
- Proxy: Caddy for per-project routing (`project.example.com -> container:port`) and automatic HTTPS
- Frontend: Next.js + TypeScript + Tailwind/shadcn (V1 uses an embedded static page served from the Go binary)
- Realtime: Server-Sent Events for deploy logs (`GET /api/deployments/{id}/logs/stream`)
- Observability: structured logs, Prometheus `/metrics`, Grafana (deployment duration, failure count, worker jobs)
- Automation: GitHub webhooks for auto-deploy on push; manual deploy and rollback via API

No framework on the backend. No ORM in V1. No Redis unless the poll-based worker needs a real queue later.

## System architecture

Target shape:

![Target architecture](docs/diagrams/target-architecture.svg)

*Editable source: [`docs/diagrams/target-architecture.excalidraw`](docs/diagrams/target-architecture.excalidraw) — open in excalidraw.com.*

```txt
User
  |
  v
Next.js Dashboard
  |
  v
Go API (net/http)
  |
  +--> PostgreSQL (projects, deployments, logs, env vars)
  |
  +--> Go Worker (polls queued jobs)
         |
         +--> git clone <repo> @ <branch>
         +--> docker build -t <project-id>:<commit-sha> .
         +--> docker run --network deploy-platform <image>
         +--> health check GET <healthCheckPath>
         +--> Caddy route update (pass: point traffic at new container / fail: keep old container)
         +--> log append + status transition
         +--> rollback to previous image on request

Internet
  |
  v
Caddy Reverse Proxy
  |
  v
Project Containers (internal ports only)
```

V1 implementation in this repo (`cmd/server/main.go`):

![V1 implementation](docs/diagrams/v1-implementation.svg)

*Editable source: [`docs/diagrams/v1-implementation.excalidraw`](docs/diagrams/v1-implementation.excalidraw).*

```txt
Embedded Dashboard
  |
  v
app.Server (internal/app/server.go)
  |
  +--> store.Store (internal/store/store.go)
  |      V1: JSONStore (.data/state.json) with in-memory pub/sub for logs
  |
  +--> worker.Worker (internal/worker/worker.go, 1s ticker, processOnce)
         |
         +--> executor.Executor (internal/executor/executor.go)
                V1: SimulationExecutor (emits clone/build/run/health-check steps)
                V2: Docker executor behind the same interface
```

The core rule: the API never runs infrastructure work. It validates input, creates a `queued` deployment row, and returns. The worker owns all state transitions. The store records every transition. This stays unchanged when JSON becomes Postgres and simulation becomes Docker.

Domain model (`internal/domain/domain.go`):

- `Project`: id, name, repoUrl, branch, healthCheckPath, liveUrl, activeDeploymentId
- `Deployment`: id, projectId, commitSha, imageTag, status, error, startedAt, finishedAt, triggeredBy, rollbackTo
- `DeploymentLog`: id, deploymentId, timestamp, stream (system/stdout/stderr), message
- Statuses: `queued -> cloning -> building -> deploying -> running_health_check -> success | failed`, plus `rollback -> success` and `rolled_back` for the replaced deployment

Deploy flow:

![Deploy flow](docs/diagrams/deploy-flow.svg)

*Editable source: [`docs/diagrams/deploy-flow.excalidraw`](docs/diagrams/deploy-flow.excalidraw).*

```txt
1. POST /api/projects/{id}/deployments -> row with status=queued
2. Worker ticker finds oldest queued/rollback job, one at a time
3. status=cloning, log "worker picked up job"
4. Executor.Deploy runs: clone, metadata check, Dockerfile strategy, build, network route, start candidate, health check
5. Each executor step calls logFunc -> Store.AddLog -> broadcast to SSE subscribers
6. On executor error: status=failed, error set, finishedAt set, old container untouched
7. On success: status=deploying -> running_health_check -> success, imageTag saved
8. Project.activeDeploymentId set to new deployment, finishedAt set
```

Rollback flow (`POST /api/deployments/{id}/rollback`):

```txt
1. API loads deployment, finds previous success via FindPreviousSuccessfulDeployment
2. Creates new row with status=rollback, rollbackTo=<target-id>
3. Worker picks it up, loads target imageTag/commitSha
4. Executor.Rollback runs: start previous image, health check, point route back
5. New row marked success (carries target imageTag/commitSha)
6. Previously active deployment marked rolled_back, Project.activeDeploymentId reset to target
7. Old working image is never deleted until the replacement passes its health check
```

Log streaming (`internal/app/server.go:streamLogs`, `internal/store/json_store.go`):

```txt
GET /api/deployments/{id}/logs/stream (text/event-stream)
1. Server replays existing ListLogs as SSE events
2. Server calls SubscribeLogs(id) -> per-deployment channel + unsubscribe func
3. Worker AddLog writes to store and non-blocking broadcast to that channel
4. Handler forwards channel messages as `event: log` until client disconnect
```

Failure handling: health check failure leaves the previous container serving traffic and marks only the candidate as failed. Rollback requires a prior success or the API returns 400. Worker failures are logged to the deployment log, not just stdout, so they are visible in the dashboard.
