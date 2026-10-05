/**
 * Typed client for the platform API.
 *
 * The dashboard is the only consumer of this API, so the types here mirror
 * internal/domain exactly. If the Go structs change, these must change too --
 * that is the cost of not generating clients, and it is worth paying for six
 * endpoints.
 */

export type DeploymentStatus =
  | "queued"
  | "cloning"
  | "building"
  | "deploying"
  | "running_health_check"
  | "success"
  | "failed"
  | "rolled_back"
  | "rollback";

export interface Project {
  id: string;
  name: string;
  repoUrl: string;
  branch: string;
  healthCheckPath: string;
  liveUrl: string;
  host: string;
  activeDeploymentId: string;
  createdAt: string;
  updatedAt: string;
}

export interface Deployment {
  id: string;
  projectId: string;
  commitSha: string;
  imageTag: string;
  status: DeploymentStatus;
  error: string;
  startedAt: string;
  finishedAt?: string;
  triggeredBy: string;
  rollbackTo?: string;
  port?: number;
}

export interface DeploymentLog {
  id: string;
  deploymentId: string;
  timestamp: string;
  stream: string;
  message: string;
}

export interface EnvVar {
  id: string;
  projectId: string;
  key: string;
  value: string;
  createdAt: string;
  updatedAt: string;
}

export class ApiError extends Error {
  readonly status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

const TOKEN_KEY = "deploy-platform-token";

/**
 * The API token, persisted in localStorage. Empty when the server runs
 * without DEPLOY_PLATFORM_API_TOKEN or before the user signs in.
 */
export function getAuthToken(): string {
  try {
    return localStorage.getItem(TOKEN_KEY) ?? "";
  } catch {
    return "";
  }
}

export function setAuthToken(token: string): void {
  try {
    if (token) {
      localStorage.setItem(TOKEN_KEY, token);
    } else {
      localStorage.removeItem(TOKEN_KEY);
    }
  } catch {
    // Private browsing: the token still works for this page load.
  }
}

function authHeaders(): Record<string, string> {
  const token = getAuthToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...authHeaders(), ...init?.headers },
  });

  if (response.status === 204) {
    return undefined as T;
  }

  let payload: unknown;
  try {
    payload = await response.json();
  } catch {
    throw new ApiError(`Unexpected response from ${path}`, response.status);
  }

  if (!response.ok) {
    const message =
      typeof payload === "object" && payload !== null && "error" in payload
        ? String((payload as { error: unknown }).error)
        : `Request to ${path} failed`;
    throw new ApiError(message, response.status);
  }
  return payload as T;
}

export const api = {
  listProjects: () => request<Project[]>("/api/projects"),

  createProject: (input: {
    name: string;
    repoUrl: string;
    branch: string;
    healthCheckPath: string;
  }) => request<Project>("/api/projects", { method: "POST", body: JSON.stringify(input) }),

  deploy: (projectId: string) =>
    request<Deployment>(`/api/projects/${projectId}/deployments`, { method: "POST" }),

  listDeployments: (projectId: string) =>
    request<Deployment[]>(`/api/projects/${projectId}/deployments`),

  rollback: (deploymentId: string) =>
    request<Deployment>(`/api/deployments/${deploymentId}/rollback`, { method: "POST" }),

  listLogs: (deploymentId: string) =>
    request<DeploymentLog[]>(`/api/deployments/${deploymentId}/logs`),

  listEnvVars: (projectId: string) => request<EnvVar[]>(`/api/projects/${projectId}/env`),

  setEnvVar: (projectId: string, key: string, value: string) =>
    request<EnvVar>(`/api/projects/${projectId}/env`, {
      method: "PUT",
      body: JSON.stringify({ key, value }),
    }),

  deleteEnvVar: (projectId: string, key: string) =>
    request<void>(`/api/projects/${projectId}/env/${encodeURIComponent(key)}`, {
      method: "DELETE",
    }),
};

/**
 * Streams deployment logs over SSE.
 *
 * The server replays the full history on connect, then pushes new lines, so the
 * caller only has to append. Browsers cannot set headers on an EventSource, so
 * the token travels as a query parameter the server explicitly accepts.
 * Returns a close function.
 */
export function streamLogs(deploymentId: string, onLog: (log: DeploymentLog) => void): () => void {
  const token = getAuthToken();
  const url = token
    ? `/api/deployments/${deploymentId}/logs/stream?access_token=${encodeURIComponent(token)}`
    : `/api/deployments/${deploymentId}/logs/stream`;
  const source = new EventSource(url);
  const handler = (event: MessageEvent<string>) => {
    try {
      onLog(JSON.parse(event.data) as DeploymentLog);
    } catch {
      // A malformed frame must not kill the stream; the next one usually works.
    }
  };
  source.addEventListener("log", handler as EventListener);
  return () => {
    source.removeEventListener("log", handler as EventListener);
    source.close();
  };
}
