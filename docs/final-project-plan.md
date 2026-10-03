# Deployment Platform — Final Project Plan

## End goal

Build a small, self-hosted deployment control plane for one VPS and one developer:

```text
GitHub push → verified webhook → queued deployment → Docker build → health check
→ proxy switch → live URL
```

It should support manual deployments, live logs, environment variables, deployment history, safe rollback, and recovery after a server restart.

This is not intended to compete with Kubernetes, Render, or Railway. Its purpose is to demonstrate practical understanding of asynchronous work, infrastructure adapters, release safety, persistence, and operational failure handling.

## Why this is necessary

The repository already contains most of the interesting building blocks. The project becomes convincing when the central deployment path is reliable end to end—not when more subsystems are added.

The important engineering guarantees are:

- A candidate must pass its health check before receiving traffic.
- A failed candidate must leave the previous version running.
- Webhooks must be authenticated, repository-specific, and idempotent.
- Background work must recover after process restarts.
- Persistent state must contain enough runtime information for routing and rollback.
- Security and operational correctness take priority over feature count.

## Implementation priorities

### 1. Make deployment correctness complete

- Match GitHub webhooks by repository and branch.
- Deduplicate repeated webhook deliveries.
- Preserve the active deployment when build, health check, or proxy changes fail.
- Persist and restore deployment ports and runtime metadata in PostgreSQL.
- Verify rollback and proxy reconstruction after a process restart.

### 2. Add minimum viable security

- Protect dashboard and deployment APIs with simple single-user authentication.
- Keep GitHub HMAC verification for webhooks.
- Do not expose environment-variable values in logs or unnecessary responses.
- Validate repository URLs, hostnames, branches, and deployment input.
- Document the single-user, single-server security model.

### 3. Make the worker recoverable

- Add ownership, lease expiry, or equivalent recovery metadata to active jobs.
- Detect deployments stuck in non-terminal states during startup.
- Retry safely or mark abandoned work failed with a clear explanation.
- Preserve the per-project single-deployment concurrency guarantee.

### 4. Complete the Docker path

- Clone the selected branch and record the actual commit SHA.
- Build a deterministic image and start an isolated candidate container.
- Inject environment variables and apply resource limits.
- Run the health check before switching the live route.
- Atomically update Caddy routing.
- Stop the previous container only after the candidate is live.
- Clean up failed containers, build directories, and obsolete images.

### 5. Keep the architecture deliberately small

Retain the Go HTTP server, Store interface, worker state machine, simulation/Docker executor seam, proxy writer, SSE logs, and embedded dashboard.

Defer Kubernetes, multiple servers, teams, billing, multi-tenancy, Redis, and a full Next.js migration. The custom metrics implementation should either be replaced by a standard Prometheus library or kept intentionally small as a learning exercise.

## Completion criteria

The project is complete when a reviewer can:

1. Start it locally with the simulation executor.
2. Create a project and trigger a deployment from the dashboard.
3. Watch deployment logs update live.
4. Run the same flow with Docker enabled.
5. Push to the configured GitHub repository and trigger exactly one deployment.
6. See a failed candidate while the previous version remains live.
7. Roll back to a previous successful deployment.
8. Restart the server and retain correct history and routing.
9. Confirm unauthenticated users cannot deploy or modify environment variables.
10. Run the Go tests and UI typecheck/build successfully.

## Final demonstration

Use one scenario for the portfolio presentation:

1. Deploy version A successfully.
2. Deploy version B successfully.
3. Deploy a deliberately broken version C.
4. Show that version B remains live.
5. Deploy a fixed version D.
6. Roll back to version B.
7. Restart the platform and show that state and routing remain correct.

The resulting story is clear: this is a focused deployment control plane designed around safe releases and failure recovery.
