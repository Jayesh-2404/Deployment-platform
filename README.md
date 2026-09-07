# Deploy Platform

A self-hosted deployment platform V1 built in Go.

This version focuses on the backend and infrastructure control-plane shape:

- project registration
- deployment records
- background worker
- deployment status transitions
- live log streaming with Server-Sent Events
- rollback to the previous successful deployment
- simple embedded dashboard
- JSON persistence for local development
- Docker executor abstraction with simulation mode

Docker, PostgreSQL, Caddy, and the Next.js dashboard can be added after this local control plane is stable.

## Run Locally

```bash
go run ./cmd/server
```

Then open:

```txt
http://localhost:8080
```

By default V1 runs in simulation mode, so Docker is not required.

## API

```txt
GET    /api/health
GET    /api/projects
POST   /api/projects
GET    /api/projects/{id}
POST   /api/projects/{id}/deployments
GET    /api/projects/{id}/deployments
GET    /api/deployments/{id}
GET    /api/deployments/{id}/logs/stream
POST   /api/deployments/{id}/rollback
```

## V1 Scope

This is not yet a complete PaaS. It is the first working slice of the architecture:

```txt
Dashboard -> Go API -> JSON Store -> Worker -> Executor -> Logs/Status
```

The next version should replace JSON persistence with PostgreSQL and connect the executor to real Docker build/run commands.
