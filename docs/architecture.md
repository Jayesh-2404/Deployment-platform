# Architecture

V1 is a local control-plane prototype for the deployment platform.

```txt
Browser Dashboard
  |
  v
Go HTTP API
  |
  +--> JSON Store
  |
  +--> Deployment rows
          |
          v
      Go Worker
          |
          v
  Simulation Executor
          |
          v
      Logs + Status
```

## Components

### Dashboard

The dashboard is embedded into the Go binary. It supports:

- creating projects
- triggering deployments
- viewing deployment history
- streaming logs
- requesting rollback

### API

The API owns HTTP routing and validates requests. It does not run deployments directly. It creates deployment records and lets the worker process them.

### Store

V1 uses `.data/state.json` for persistence. This keeps the first version easy to run without PostgreSQL.

The store interface is separated so PostgreSQL can replace JSON persistence in V2.

### Worker

The worker polls for queued deployment records and processes them in the background.

This keeps deployment work out of the API request lifecycle.

### Executor

V1 uses a simulation executor because Docker is not always available in the development environment.

The executor boundary is where real Docker support should be added later:

- clone repository
- build Docker image
- start candidate container
- run health check
- update reverse proxy route

## Why This Shape

The important architectural rule is:

> The API decides what should happen. The worker performs the infrastructure operation. The store records every state transition.

That structure will still work when JSON becomes PostgreSQL and the simulation executor becomes Docker.
