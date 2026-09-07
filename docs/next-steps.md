# Next Steps

## V2: Real Persistence

Replace the JSON store with PostgreSQL.

Suggested tables:

- `projects`
- `deployments`
- `deployment_logs`
- `environment_variables`

## V3: Docker Executor

Replace `SimulationExecutor` with a real Docker executor.

First Docker implementation:

```txt
git clone <repo>
docker build -t <image-tag> .
docker run --name <container-name> --network deploy-platform <image-tag>
```

Then add:

- env var injection
- build directory cleanup
- image cleanup
- container resource limits
- health-check failure handling

## V4: Reverse Proxy

Add Caddy routing:

```txt
project.example.com -> project-container:3000
```

Keep Caddy config generation inside the worker because it is part of deployment state changes.

## V5: Observability

Add:

- `/metrics`
- deployment duration histogram
- deployment failure counter
- worker job counter
- Grafana dashboard

## V6: GitHub Webhooks

Add automatic deployment on push to the selected branch.
