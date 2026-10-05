import { render } from "preact";
import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import {
  api,
  getAuthToken,
  setAuthToken,
  streamLogs,
  type Deployment,
  type DeploymentLog,
  type EnvVar,
  type Project,
} from "./api";
import {
  Button,
  EmptyState,
  ErrorBanner,
  Panel,
  StatusBadge,
  duration,
  shortSha,
} from "./components";

/** How often the dashboard re-reads project and deployment state. */
const POLL_MS = 2500;

/**
 * The dashboard root.
 *
 * State is deliberately minimal: the server owns deployment state, so the UI
 * only holds the current selection, the last fetched data, and the live log
 * buffer. Polling plus SSE is enough for a six-screen admin tool and avoids
 * inventing a client-side cache that could disagree with the server.
 */
function App() {
  const [projects, setProjects] = useState<Project[]>([]);
  const [selectedProjectId, setSelectedProjectId] = useState("");
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [selectedDeploymentId, setSelectedDeploymentId] = useState("");
  const [logs, setLogs] = useState<DeploymentLog[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  const report = useCallback((err: unknown) => {
    setError(err instanceof Error ? err.message : String(err));
  }, []);

  // Refresh projects and the selected project's deployments on a timer.
  useEffect(() => {
    let cancelled = false;

    const refresh = async () => {
      try {
        const next = await api.listProjects();
        if (cancelled) return;
        setProjects(next);
        setError("");

        setSelectedProjectId((current) => {
          if (current && next.some((p) => p.id === current)) return current;
          return next[0]?.id ?? "";
        });
      } catch (err) {
        if (!cancelled) report(err);
      } finally {
        if (!cancelled) setLoading(false);
      }
    };

    void refresh();
    const timer = window.setInterval(refresh, POLL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [report]);

  useEffect(() => {
    if (!selectedProjectId) {
      setDeployments([]);
      return;
    }
    let cancelled = false;

    const refresh = async () => {
      try {
        const next = await api.listDeployments(selectedProjectId);
        if (!cancelled) setDeployments(next);
      } catch (err) {
        if (!cancelled) report(err);
      }
    };

    void refresh();
    const timer = window.setInterval(refresh, POLL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [selectedProjectId, report]);

  const selectedProject = projects.find((p) => p.id === selectedProjectId) ?? null;
  const selectedDeployment = deployments.find((d) => d.id === selectedDeploymentId) ?? null;

  // Live logs for the selected deployment.
  useEffect(() => {
    if (!selectedDeploymentId) {
      setLogs([]);
      return;
    }
    let close: (() => void) | undefined;
    let cancelled = false;

    void (async () => {
      try {
        const history = await api.listLogs(selectedDeploymentId);
        if (cancelled) return;
        setLogs(history);
        close = streamLogs(selectedDeploymentId, (entry) => {
          setLogs((current) => {
            // The server replays history on connect and also pushes live lines,
            // so de-duplicate by log id to survive the overlap.
            if (current.some((existing) => existing.id === entry.id)) return current;
            return [...current, entry];
          });
        });
      } catch (err) {
        if (!cancelled) report(err);
      }
    })();

    return () => {
      cancelled = true;
      close?.();
    };
  }, [selectedDeploymentId, report]);

  const onCreateProject = async (input: {
    name: string;
    repoUrl: string;
    branch: string;
    healthCheckPath: string;
  }) => {
    const project = await api.createProject(input);
    setSelectedProjectId(project.id);
    setError("");
  };

  const onDeploy = async (projectId: string) => {
    try {
      const deployment = await api.deploy(projectId);
      setSelectedDeploymentId(deployment.id);
      setError("");
    } catch (err) {
      report(err);
    }
  };

  const onRollback = async (deploymentId: string) => {
    try {
      const rollback = await api.rollback(deploymentId);
      setSelectedDeploymentId(rollback.id);
      setError("");
    } catch (err) {
      report(err);
    }
  };

  return (
    <div class="shell">
      <header class="topbar">
        <div>
          <p class="topbar__eyebrow">Self-hosted deployment control plane</p>
          <h1>Deploy Platform</h1>
        </div>
        <p class="topbar__summary">
          {loading ? "Loading..." : `${projects.length} project${projects.length === 1 ? "" : "s"}`}
        </p>
      </header>

      {error ? <ErrorBanner message={error} onDismiss={() => setError("")} /> : null}

      <div class="layout">
        <div class="layout__side">
          <AuthPanel report={report} />
          <NewProjectForm onSubmit={onCreateProject} report={report} />
          <Panel title="Projects" meta={selectedProject ? `selected: ${selectedProject.name}` : undefined}>
            {projects.length === 0 ? (
              <EmptyState title="No projects yet" hint="Create one to start deploying." />
            ) : (
              <ul class="cards">
                {projects.map((project) => (
                  <ProjectCard
                    key={project.id}
                    project={project}
                    active={project.id === selectedProjectId}
                    deployments={project.id === selectedProjectId ? deployments : []}
                    onSelect={() => {
                      setSelectedProjectId(project.id);
                      setSelectedDeploymentId("");
                    }}
                    onDeploy={() => onDeploy(project.id)}
                  />
                ))}
              </ul>
            )}
          </Panel>
        </div>

        <div class="layout__main">
          <Panel
            title="Deployments"
            meta={selectedProject ? selectedProject.name : "no project selected"}
          >
            {deployments.length === 0 ? (
              <EmptyState
                title="No deployments yet"
                hint="Press Deploy on a project to run one."
              />
            ) : (
              <ul class="rows">
                {deployments.map((deployment) => (
                  <DeploymentRow
                    key={deployment.id}
                    deployment={deployment}
                    active={deployment.id === selectedDeploymentId}
                    onSelect={() => setSelectedDeploymentId(deployment.id)}
                    onRollback={() => onRollback(deployment.id)}
                  />
                ))}
              </ul>
            )}
          </Panel>

          <Panel
            title="Live logs"
            meta={selectedDeployment ? shortSha(selectedDeployment.commitSha) : "no deployment selected"}
          >
            <LogView logs={logs} />
          </Panel>

          {selectedProject ? <EnvPanel project={selectedProject} report={report} /> : null}
        </div>
      </div>
    </div>
  );
}

function ProjectCard(props: {
  project: Project;
  active: boolean;
  deployments: Deployment[];
  onSelect: () => void;
  onDeploy: () => void;
}) {
  const { project, active, deployments, onSelect, onDeploy } = props;
  const live = deployments.find((d) => d.id === project.activeDeploymentId);
  const latest = deployments[0];
  const inFlight = latest && !["success", "failed", "rolled_back"].includes(latest.status);

  return (
    <li class={`card ${active ? "card--active" : ""}`}>
      <button type="button" class="card__main" onClick={onSelect}>
        <span class="card__name">{project.name}</span>
        <span class="card__url">{project.liveUrl}</span>
        <span class="card__meta">
          {project.branch} &middot; {project.healthCheckPath}
        </span>
      </button>
      <div class="card__footer">
        {live ? <StatusBadge status={live.status} /> : <span class="badge badge--new">new</span>}
        <Button
          type="button"
          variant="primary"
          onClick={onDeploy}
          disabled={Boolean(inFlight)}
          title={inFlight ? "A deployment is already running" : undefined}
        >
          {inFlight ? "Deploying" : "Deploy"}
        </Button>
      </div>
    </li>
  );
}

function DeploymentRow(props: {
  deployment: Deployment;
  active: boolean;
  onSelect: () => void;
  onRollback: () => void;
}) {
  const { deployment, active, onSelect, onRollback } = props;
  const canRollback = deployment.status === "success";

  return (
    <li class={`row ${active ? "row--active" : ""}`}>
      <button type="button" class="row__main" onClick={onSelect}>
        <span class="row__id">{shortSha(deployment.commitSha)}</span>
        <StatusBadge status={deployment.status} />
        <span class="row__meta">
          {deployment.triggeredBy} &middot; {duration(deployment.startedAt, deployment.finishedAt)}
          {deployment.port ? ` · :${deployment.port}` : ""}
        </span>
      </button>
      {deployment.error ? <p class="row__error">{deployment.error}</p> : null}
      <div class="row__actions">
        <Button type="button" onClick={onSelect}>
          Logs
        </Button>
        <Button type="button" variant="danger" onClick={onRollback} disabled={!canRollback}>
          Rollback
        </Button>
      </div>
    </li>
  );
}

/** LogView is a fixed-height scroller so streaming output does not grow the page. */
function LogView({ logs }: { logs: DeploymentLog[] }) {
  const scroller = useRef<HTMLPreElement>(null);
  const pinned = useRef(true);

  // Follow new output only while the reader is already at the bottom, so
  // scrolling back to read an error is not yanked away by incoming lines.
  useEffect(() => {
    const element = scroller.current;
    if (element && pinned.current) {
      element.scrollTop = element.scrollHeight;
    }
  }, [logs]);

  if (logs.length === 0) {
    return <EmptyState title="No log output" hint="Select a deployment to stream its logs." />;
  }

  return (
    <pre
      class="logs"
      ref={scroller}
      onScroll={(event) => {
        const element = event.currentTarget;
        pinned.current = element.scrollHeight - element.scrollTop - element.clientHeight < 24;
      }}
    >
      {logs.map((log) => (
        <span key={log.id} class={`log log--${log.stream}`}>
          <span class="log__time">{new Date(log.timestamp).toLocaleTimeString()}</span>
          <span class="log__stream">{log.stream}</span>
          <span class="log__message">{log.message}</span>
        </span>
      ))}
    </pre>
  );
}

/** AuthPanel stores the API bearer token for servers that require one. */
function AuthPanel(props: { report: (err: unknown) => void }) {
  const [token, setToken] = useState(getAuthToken());
  const [saved, setSaved] = useState(() => getAuthToken() !== "");

  const save = (event: Event) => {
    event.preventDefault();
    try {
      setAuthToken(token.trim());
      setSaved(token.trim() !== "");
      setToken(token.trim());
    } catch (err) {
      props.report(err);
    }
  };

  const clear = () => {
    setAuthToken("");
    setToken("");
    setSaved(false);
  };

  return (
    <Panel title="Access token" meta={saved ? "signed in" : "no token saved"}>
      {saved ? (
        <div class="form">
          <p class="panel__meta">Requests are authenticated with the saved token.</p>
          <Button type="button" onClick={clear}>
            Sign out
          </Button>
        </div>
      ) : (
        <form class="form" onSubmit={save}>
          <label class="field">
            <span>API token</span>
            <input
              type="password"
              value={token}
              onInput={(event) => setToken((event.currentTarget as HTMLInputElement).value)}
              placeholder="DEPLOY_PLATFORM_API_TOKEN"
              autoComplete="off"
            />
          </label>
          <Button type="submit" variant="primary">
            Sign in
          </Button>
        </form>
      )}
    </Panel>
  );
}

function NewProjectForm(props: {
  onSubmit: (input: {
    name: string;
    repoUrl: string;
    branch: string;
    healthCheckPath: string;
  }) => Promise<void>;
  report: (err: unknown) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [fields, setFields] = useState({
    name: "",
    repoUrl: "",
    branch: "main",
    healthCheckPath: "/",
  });

  const update = (key: keyof typeof fields) => (event: Event) => {
    const target = event.currentTarget as HTMLInputElement;
    setFields((current) => ({ ...current, [key]: target.value }));
  };

  const submit = async (event: Event) => {
    event.preventDefault();
    setBusy(true);
    try {
      await props.onSubmit({
        name: fields.name.trim(),
        repoUrl: fields.repoUrl.trim(),
        branch: fields.branch.trim() || "main",
        healthCheckPath: fields.healthCheckPath.trim() || "/",
      });
      setFields({ name: "", repoUrl: "", branch: "main", healthCheckPath: "/" });
    } catch (err) {
      props.report(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Panel title="Add project">
      <form class="form" onSubmit={submit}>
        <label class="field">
          <span>Name</span>
          <input
            required
            value={fields.name}
            onInput={update("name")}
            placeholder="PayApp"
            autoComplete="off"
          />
        </label>
        <label class="field">
          <span>Repository URL</span>
          <input
            required
            value={fields.repoUrl}
            onInput={update("repoUrl")}
            placeholder="https://github.com/you/payapp"
            autoComplete="off"
          />
        </label>
        <div class="field-row">
          <label class="field">
            <span>Branch</span>
            <input value={fields.branch} onInput={update("branch")} autoComplete="off" />
          </label>
          <label class="field">
            <span>Health path</span>
            <input
              value={fields.healthCheckPath}
              onInput={update("healthCheckPath")}
              autoComplete="off"
            />
          </label>
        </div>
        <Button type="submit" variant="primary" disabled={busy}>
          {busy ? "Creating..." : "Create project"}
        </Button>
      </form>
    </Panel>
  );
}

/** EnvPanel edits the environment injected into the container at deploy time. */
function EnvPanel(props: { project: Project; report: (err: unknown) => void }) {
  const [vars, setVars] = useState<EnvVar[]>([]);
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");

  const reload = useCallback(async () => {
    try {
      setVars(await api.listEnvVars(props.project.id));
    } catch (err) {
      props.report(err);
    }
  }, [props.project.id, props.report]);

  useEffect(() => {
    void reload();
  }, [reload]);

  const add = async (event: Event) => {
    event.preventDefault();
    if (!key.trim()) return;
    try {
      await api.setEnvVar(props.project.id, key.trim(), value);
      setKey("");
      setValue("");
      await reload();
    } catch (err) {
      props.report(err);
    }
  };

  const remove = async (name: string) => {
    try {
      await api.deleteEnvVar(props.project.id, name);
      await reload();
    } catch (err) {
      props.report(err);
    }
  };

  return (
    <Panel title="Environment" meta={`injected at deploy time into ${props.project.name}`}>
      {vars.length > 0 ? (
        <ul class="rows rows--compact">
          {vars.map((item) => (
            <li key={item.id} class="row row--compact">
              <code class="row__id">{item.key}</code>
              <span class="row__meta row__meta--value">{item.value}</span>
              <Button type="button" variant="danger" onClick={() => void remove(item.key)}>
                Remove
              </Button>
            </li>
          ))}
        </ul>
      ) : (
        <EmptyState title="No environment variables" hint="Add one to configure the container." />
      )}

      <form class="form form--inline" onSubmit={add}>
        <input
          required
          value={key}
          placeholder="DATABASE_URL"
          onInput={(event) => setKey((event.currentTarget as HTMLInputElement).value)}
          autoComplete="off"
        />
        <input
          value={value}
          placeholder="postgres://..."
          onInput={(event) => setValue((event.currentTarget as HTMLInputElement).value)}
          autoComplete="off"
        />
        <Button type="submit">Add</Button>
      </form>
    </Panel>
  );
}

const root = document.querySelector("#root");
if (root) {
  render(<App />, root);
}
