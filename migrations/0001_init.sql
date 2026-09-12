-- V2 baseline schema. Mirrors internal/domain types.
-- Run with: psql "$DATABASE_URL" -f migrations/0001_init.sql

CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  repo_url TEXT NOT NULL,
  branch TEXT NOT NULL DEFAULT 'main',
  health_check_path TEXT NOT NULL DEFAULT '/',
  live_url TEXT NOT NULL,
  active_deployment_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS deployments (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  commit_sha TEXT NOT NULL DEFAULT '',
  image_tag TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'queued',
  error TEXT NOT NULL DEFAULT '',
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at TIMESTAMPTZ,
  triggered_by TEXT NOT NULL DEFAULT 'manual',
  rollback_to TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_deployments_project_started
  ON deployments (project_id, started_at DESC);

CREATE TABLE IF NOT EXISTS deployment_logs (
  id TEXT PRIMARY KEY,
  deployment_id TEXT NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
  timestamp TIMESTAMPTZ NOT NULL DEFAULT now(),
  stream TEXT NOT NULL DEFAULT 'system',
  message TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_deployment_logs_deployment_time
  ON deployment_logs (deployment_id, timestamp ASC);

CREATE TABLE IF NOT EXISTS environment_variables (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  value TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (project_id, key)
);
