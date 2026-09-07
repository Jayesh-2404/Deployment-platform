let selectedProjectId = "";
let selectedDeploymentId = "";
let logStream = null;

const els = {
  form: document.querySelector("#projectForm"),
  projects: document.querySelector("#projects"),
  deployments: document.querySelector("#deployments"),
  logs: document.querySelector("#logs"),
  projectCount: document.querySelector("#projectCount"),
  selectedProjectName: document.querySelector("#selectedProjectName"),
  selectedDeploymentName: document.querySelector("#selectedDeploymentName"),
  refreshButton: document.querySelector("#refreshButton"),
};

els.form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = new FormData(els.form);
  const payload = {
    name: form.get("name"),
    repoUrl: form.get("repoUrl"),
    branch: form.get("branch") || "main",
    healthCheckPath: form.get("healthCheckPath") || "/",
  };

  const project = await request("/api/projects", {
    method: "POST",
    body: JSON.stringify(payload),
  });
  els.form.reset();
  selectedProjectId = project.id;
  await load();
});

els.refreshButton.addEventListener("click", load);

async function load() {
  const projects = await request("/api/projects");
  renderProjects(projects);

  if (!selectedProjectId && projects.length > 0) {
    selectedProjectId = projects[0].id;
  }

  const selectedProject = projects.find((project) => project.id === selectedProjectId);
  if (selectedProject) {
    els.selectedProjectName.textContent = selectedProject.name;
    const deployments = await request(`/api/projects/${selectedProject.id}/deployments`);
    renderDeployments(deployments);
  } else {
    els.selectedProjectName.textContent = "No project selected";
    els.deployments.innerHTML = "";
  }
}

function renderProjects(projects) {
  els.projectCount.textContent = String(projects.length);
  els.projects.innerHTML = projects.map((project) => `
    <article class="item ${project.id === selectedProjectId ? "active" : ""}">
      <div class="item-header">
        <div>
          <h3>${escapeHTML(project.name)}</h3>
          <p>${escapeHTML(project.repoUrl)}</p>
        </div>
        ${project.activeDeploymentId ? `<span class="status success">active</span>` : `<span class="status queued">new</span>`}
      </div>
      <p>${escapeHTML(project.liveUrl)} · branch ${escapeHTML(project.branch)}</p>
      <div class="actions">
        <button class="secondary" onclick="selectProject('${project.id}')">Open</button>
        <button onclick="deployProject('${project.id}')">Deploy</button>
      </div>
    </article>
  `).join("");
}

function renderDeployments(deployments) {
  els.deployments.innerHTML = deployments.map((deployment) => `
    <article class="item ${deployment.id === selectedDeploymentId ? "active" : ""}">
      <div class="item-header">
        <div>
          <h3>${deployment.id}</h3>
          <p>commit ${escapeHTML(deployment.commitSha)} · ${new Date(deployment.startedAt).toLocaleString()}</p>
        </div>
        <span class="status ${deployment.status}">${deployment.status}</span>
      </div>
      ${deployment.error ? `<p>${escapeHTML(deployment.error)}</p>` : ""}
      <div class="actions">
        <button class="secondary" onclick="selectDeployment('${deployment.id}')">Logs</button>
        <button class="danger" onclick="rollbackDeployment('${deployment.id}')" ${deployment.status !== "success" ? "disabled" : ""}>Rollback</button>
      </div>
    </article>
  `).join("") || "<p>No deployments yet.</p>";
}

async function selectProject(projectId) {
  selectedProjectId = projectId;
  selectedDeploymentId = "";
  closeLogStream();
  els.logs.textContent = "Select a deployment to stream logs.";
  await load();
}

async function deployProject(projectId) {
  const deployment = await request(`/api/projects/${projectId}/deployments`, { method: "POST" });
  selectedDeploymentId = deployment.id;
  await load();
  selectDeployment(deployment.id);
}

async function selectDeployment(deploymentId) {
  selectedDeploymentId = deploymentId;
  els.selectedDeploymentName.textContent = deploymentId;
  els.logs.textContent = "";
  closeLogStream();

  logStream = new EventSource(`/api/deployments/${deploymentId}/logs/stream`);
  logStream.addEventListener("log", (event) => {
    const log = JSON.parse(event.data);
    els.logs.textContent += `[${new Date(log.timestamp).toLocaleTimeString()}] ${log.stream}: ${log.message}\n`;
    els.logs.scrollTop = els.logs.scrollHeight;
  });
}

async function rollbackDeployment(deploymentId) {
  const rollback = await request(`/api/deployments/${deploymentId}/rollback`, { method: "POST" });
  selectedDeploymentId = rollback.id;
  await load();
  selectDeployment(rollback.id);
}

function closeLogStream() {
  if (logStream) {
    logStream.close();
    logStream = null;
  }
}

async function request(path, options = {}) {
  const response = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  const body = await response.json();
  if (!response.ok) {
    throw new Error(body.error || "Request failed");
  }
  return body;
}

function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

load();
setInterval(load, 2500);
