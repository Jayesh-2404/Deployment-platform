package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"deploy-platform/internal/domain"
	"deploy-platform/internal/metrics"
	"deploy-platform/internal/store"
)

func newTestServer(t *testing.T, configure func(*store.JSONStore)) *httptest.Server {
	t.Helper()
	repository, err := store.NewJSONStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if configure != nil {
		configure(repository)
	}
	registry := &metrics.Registry{}
	server := httptest.NewServer(NewServer(Dependencies{
		Store:     repository,
		Registry:  registry,
		Telemetry: NewTelemetry(registry),
	}))
	t.Cleanup(server.Close)
	return server
}

func doJSON(t *testing.T, server *httptest.Server, method string, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = strings.NewReader(string(encoded))
	} else {
		reader = strings.NewReader("")
	}
	request, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })

	decoded := map[string]any{}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return response, nil
	}
	return response, decoded
}

func TestHealthEndpoint(t *testing.T) {
	server := newTestServer(t, nil)
	response, _ := doJSON(t, server, http.MethodGet, "/api/health", nil)
	if response.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", response.StatusCode)
	}
}

func TestCreateProjectAndReadBack(t *testing.T) {
	server := newTestServer(t, nil)
	response, body := doJSON(t, server, http.MethodPost, "/api/projects", domain.CreateProjectInput{
		Name:            "My App",
		RepoURL:         "https://github.com/example/my-app.git",
		Branch:          "main",
		HealthCheckPath: "/healthz",
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %v", response.StatusCode, body)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("expected an id, got %v", body)
	}
	if body["liveUrl"] != "https://my-app.localhost" {
		t.Errorf("liveUrl = %v, want https://my-app.localhost", body["liveUrl"])
	}
	if body["host"] != "my-app.localhost" {
		t.Errorf("host = %v, want my-app.localhost", body["host"])
	}

	response, body = doJSON(t, server, http.MethodGet, "/api/projects/"+id, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get project status = %d, want 200", response.StatusCode)
	}
	if body["name"] != "My App" {
		t.Errorf("name = %v, want My App", body["name"])
	}
}

func TestCreateProjectRejectsBadInput(t *testing.T) {
	server := newTestServer(t, nil)
	tests := []struct {
		name  string
		input domain.CreateProjectInput
	}{
		{"missing name", domain.CreateProjectInput{RepoURL: "https://github.com/a/b.git"}},
		{"missing repo", domain.CreateProjectInput{Name: "a"}},
		{"bad repo url", domain.CreateProjectInput{Name: "a", RepoURL: "not a url"}},
		{"non-https repo url", domain.CreateProjectInput{Name: "a", RepoURL: "git@github.com:a/b.git"}},
		{"non-github repo url", domain.CreateProjectInput{Name: "a", RepoURL: "https://gitlab.com/a/b.git"}},
		{"repo url without owner and repo", domain.CreateProjectInput{Name: "a", RepoURL: "https://github.com/onlyowner"}},
		{"repo url with query string", domain.CreateProjectInput{Name: "a", RepoURL: "https://github.com/a/b?token=x"}},
		{"branch with leading dash", domain.CreateProjectInput{
			Name: "a", RepoURL: "https://github.com/a/b.git", Branch: "-evil",
		}},
		{"branch with spaces", domain.CreateProjectInput{
			Name: "a", RepoURL: "https://github.com/a/b.git", Branch: "my branch",
		}},
		{"branch with parent segment", domain.CreateProjectInput{
			Name: "a", RepoURL: "https://github.com/a/b.git", Branch: "a/../b",
		}},
		{"health path without slash", domain.CreateProjectInput{
			Name: "a", RepoURL: "https://github.com/a/b.git", HealthCheckPath: "healthz",
		}},
		{"health path with parent segment", domain.CreateProjectInput{
			Name: "a", RepoURL: "https://github.com/a/b.git", HealthCheckPath: "/../secret",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, body := doJSON(t, server, http.MethodPost, "/api/projects", tt.input)
			if response.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %v", response.StatusCode, body)
			}
		})
	}
}

// TestListEndpointsReturnEmptyArrayNotNull pins the JSON shape of every list
// endpoint.
//
// The bug this guards against is subtle and severe: Go encodes a nil slice as
// JSON `null`, not `[]`. A project with no deployments or no environment
// variables therefore returned `null`, and the dashboard -- which iterates
// these responses -- threw on first render of an empty project.
func TestListEndpointsReturnEmptyArrayNotNull(t *testing.T) {
	server := newTestServer(t, nil)
	response, _ := doJSON(t, server, http.MethodPost, "/api/projects", domain.CreateProjectInput{
		Name: "empty-app", RepoURL: "https://github.com/example/empty-app.git", Branch: "main",
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create project status = %d", response.StatusCode)
	}

	// Recreate the project directly so we know its id without parsing.
	projects := fetchRawList(t, server, "/api/projects")
	if len(projects) == 0 {
		t.Fatal("expected the created project to be listed")
	}
	id := projects[0]["id"].(string)

	tests := []struct {
		name string
		path string
	}{
		{"deployments", "/api/projects/" + id + "/deployments"},
		{"environment variables", "/api/projects/" + id + "/env"},
		{"logs for an unknown deployment", "/api/deployments/dep_missing/logs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fetchRawBody(t, server, tt.path)
			if strings.TrimSpace(body) != "[]" {
				t.Errorf("%s returned %q, want an empty JSON array []", tt.path, body)
			}
		})
	}
}

// fetchRawBody returns the response body verbatim, because a decoding helper
// would hide the difference between null and [].
func fetchRawBody(t *testing.T, server *httptest.Server, path string) string {
	t.Helper()
	response, err := server.Client().Get(server.URL + path)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	defer response.Body.Close()
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(response.Body); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return buffer.String()
}

func fetchRawList(t *testing.T, server *httptest.Server, path string) []map[string]any {
	t.Helper()
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(fetchRawBody(t, server, path)), &decoded); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return decoded
}

func TestGetMissingProjectReturns404(t *testing.T) {
	server := newTestServer(t, nil)
	response, _ := doJSON(t, server, http.MethodGet, "/api/projects/proj_missing", nil)
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", response.StatusCode)
	}
}

func TestEnvVarLifecycle(t *testing.T) {
	var projectID string
	repository, _ := store.NewJSONStore(t.TempDir() + "/state.json")
	project, err := repository.CreateProject(domain.CreateProjectInput{
		Name: "app", RepoURL: "https://github.com/a/b.git",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	projectID = project.ID

	registry := &metrics.Registry{}
	server := httptest.NewServer(NewServer(Dependencies{Store: repository, Registry: registry}))
	defer server.Close()

	// Set two variables.
	for _, item := range []EnvVarInput{{Key: "LOG_LEVEL", Value: "info"}, {Key: "API_KEY", Value: "secret"}} {
		response, body := doJSON(t, server, http.MethodPut, "/api/projects/"+projectID+"/env", item)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("upsert env status = %d, want 200: %v", response.StatusCode, body)
		}
	}

	response, body := doJSON(t, server, http.MethodGet, "/api/projects/"+projectID+"/env", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list env status = %d", response.StatusCode)
	}
	vars, _ := body["0"] // sanity: JSON array decodes into a map index only if object; use raw below
	_ = vars

	// Upsert must update, not duplicate.
	response, _ = doJSON(t, server, http.MethodPut, "/api/projects/"+projectID+"/env", EnvVarInput{Key: "LOG_LEVEL", Value: "debug"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("second upsert status = %d", response.StatusCode)
	}
	stored, err := repository.ListEnvVars(projectID)
	if err != nil {
		t.Fatalf("list env vars: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("expected 2 env vars after upsert, got %d: %+v", len(stored), stored)
	}
	for _, item := range stored {
		if item.Key == "LOG_LEVEL" && item.Value != "debug" {
			t.Errorf("LOG_LEVEL = %q, want debug", item.Value)
		}
	}

	// Reject an invalid key.
	response, _ = doJSON(t, server, http.MethodPut, "/api/projects/"+projectID+"/env", EnvVarInput{Key: "bad key", Value: "x"})
	if response.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid key status = %d, want 400", response.StatusCode)
	}

	// Delete.
	response = doJSONNoBody(t, server, http.MethodDelete, "/api/projects/"+projectID+"/env/API_KEY", nil)
	if response.StatusCode != http.StatusNoContent {
		t.Errorf("delete status = %d, want 204", response.StatusCode)
	}
	stored, _ = repository.ListEnvVars(projectID)
	if len(stored) != 1 {
		t.Errorf("expected 1 env var after delete, got %d", len(stored))
	}

	// Deleting a missing key is a 404.
	response = doJSONNoBody(t, server, http.MethodDelete, "/api/projects/"+projectID+"/env/NOPE", nil)
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("delete missing status = %d, want 404", response.StatusCode)
	}
}

func doJSONNoBody(t *testing.T, server *httptest.Server, method string, path string, body any) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, server.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestMetricsEndpointExposesDeploymentCounters(t *testing.T) {
	server := newTestServer(t, nil)
	response, err := server.Client().Get(server.URL + "/metrics")
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", got)
	}
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(response.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := buffer.String()
	if !strings.Contains(body, "# TYPE deploy_platform_deployments_finished_total counter") {
		t.Errorf("metrics output missing the finished counter TYPE line:\n%s", body)
	}
	if !strings.Contains(body, "deploy_platform_projects ") {
		t.Errorf("metrics output missing the projects gauge:\n%s", body)
	}
}

func TestListProjectsRefreshesProjectGauge(t *testing.T) {
	server := newTestServer(t, nil)
	for _, name := range []string{"one", "two"} {
		doJSON(t, server, http.MethodPost, "/api/projects", domain.CreateProjectInput{
			Name: name, RepoURL: "https://github.com/example/" + name + ".git",
		})
	}
	doJSON(t, server, http.MethodGet, "/api/projects", nil)

	response, err := server.Client().Get(server.URL + "/metrics")
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	defer response.Body.Close()
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(response.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(buffer.String(), "deploy_platform_projects 2") {
		t.Errorf("expected the gauge to report 2 projects, got:\n%s", buffer.String())
	}
}

// --- GitHub webhook ---

func signedRequest(t *testing.T, server *httptest.Server, secret string, event string, payload any) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(encoded)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/webhooks/github", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Event", event)
	request.Header.Set("X-Hub-Signature-256", signature)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("post webhook: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func webhookServer(t *testing.T, secret string, configure func(*store.JSONStore)) *httptest.Server {
	t.Helper()
	repository, err := store.NewJSONStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if configure != nil {
		configure(repository)
	}
	server := httptest.NewServer(NewServer(Dependencies{Store: repository, WebhookSecret: secret}))
	t.Cleanup(server.Close)
	return server
}

func pushPayload(ref string, after string) map[string]any {
	return map[string]any{
		"ref":    ref,
		"after":  after,
		"before": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"repository": map[string]any{
			"full_name": "example/my-app",
			"clone_url": "https://github.com/example/my-app.git",
		},
		"head_commit": map[string]any{"id": after, "message": "change"},
	}
}

func TestWebhookQueuesDeploymentForMatchingBranch(t *testing.T) {
	repository, err := store.NewJSONStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	project, err := repository.CreateProject(domain.CreateProjectInput{
		Name: "my-app", RepoURL: "https://github.com/example/my-app.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	registry := &metrics.Registry{}
	server := httptest.NewServer(NewServer(Dependencies{
		Store: repository, Registry: registry, WebhookSecret: "topsecret",
	}))
	defer server.Close()

	response := signedRequest(t, server, "topsecret", "push", pushPayload("refs/heads/main", "abc123def456"))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", response.StatusCode)
	}

	deployments, err := repository.ListDeployments(project.ID)
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	if len(deployments) != 1 {
		t.Fatalf("expected exactly 1 queued deployment, got %d", len(deployments))
	}
	queued := deployments[0]
	if queued.Status != domain.StatusQueued {
		t.Errorf("status = %q, want queued", queued.Status)
	}
	if queued.TriggeredBy != "webhook" {
		t.Errorf("TriggeredBy = %q, want webhook", queued.TriggeredBy)
	}
	// The pushed commit must be recorded, or history cannot say what shipped.
	if queued.CommitSHA != "abc123def456" {
		t.Errorf("CommitSHA = %q, want the pushed sha abc123def456", queued.CommitSHA)
	}

	logs, err := repository.ListLogs(queued.ID)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logs) == 0 || !strings.Contains(logs[0].Message, "abc123def456") {
		t.Errorf("first log line should name the pushed commit, got %+v", logs)
	}
}

func TestWebhookOnlyTriggersProjectsOnThePushedBranch(t *testing.T) {
	repository, err := store.NewJSONStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	main, err := repository.CreateProject(domain.CreateProjectInput{
		Name: "main-app", RepoURL: "https://github.com/example/my-app.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	release, err := repository.CreateProject(domain.CreateProjectInput{
		Name: "release-app", RepoURL: "https://github.com/example/my-app.git", Branch: "release",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	registry := &metrics.Registry{}
	server := httptest.NewServer(NewServer(Dependencies{
		Store: repository, Registry: registry, WebhookSecret: "topsecret",
	}))
	defer server.Close()

	response := signedRequest(t, server, "topsecret", "push", pushPayload("refs/heads/main", "abc123"))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", response.StatusCode)
	}

	mainDeployments, _ := repository.ListDeployments(main.ID)
	releaseDeployments, _ := repository.ListDeployments(release.ID)
	if len(mainDeployments) != 1 {
		t.Errorf("the main-branch project should have 1 deployment, got %d", len(mainDeployments))
	}
	if len(releaseDeployments) != 0 {
		t.Errorf("the release-branch project must not be deployed by a main push, got %d", len(releaseDeployments))
	}
}

func TestWebhookIgnoresBranchDeletion(t *testing.T) {
	repository, err := store.NewJSONStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	project, err := repository.CreateProject(domain.CreateProjectInput{
		Name: "my-app", RepoURL: "https://github.com/example/my-app.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	registry := &metrics.Registry{}
	server := httptest.NewServer(NewServer(Dependencies{
		Store: repository, Registry: registry, WebhookSecret: "topsecret",
	}))
	defer server.Close()

	// A deletion sends an all-zero `after` sha.
	response := signedRequest(t, server, "topsecret", "push", pushPayload("refs/heads/main", "0000000000000000000000000000000000000000"))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", response.StatusCode)
	}
	deployments, _ := repository.ListDeployments(project.ID)
	if len(deployments) != 0 {
		t.Errorf("a branch deletion must not trigger a deployment, got %d", len(deployments))
	}
}

func TestWebhookRejectsTamperedPayload(t *testing.T) {
	repository, err := store.NewJSONStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	project, err := repository.CreateProject(domain.CreateProjectInput{
		Name: "my-app", RepoURL: "https://github.com/example/my-app.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	registry := &metrics.Registry{}
	server := httptest.NewServer(NewServer(Dependencies{
		Store: repository, Registry: registry, WebhookSecret: "topsecret",
	}))
	defer server.Close()

	// Sign one payload, then send a different one.
	encoded, _ := json.Marshal(pushPayload("refs/heads/main", "abc123"))
	mac := hmac.New(sha256.New, []byte("topsecret"))
	mac.Write(encoded)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	tampered := pushPayload("refs/heads/main", "deadbeef")
	tamperedBytes, _ := json.Marshal(tampered)
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/webhooks/github", bytes.NewReader(tamperedBytes))
	request.Header.Set("X-GitHub-Event", "push")
	request.Header.Set("X-Hub-Signature-256", signature)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a tampered payload", response.StatusCode)
	}
	deployments, _ := repository.ListDeployments(project.ID)
	if len(deployments) != 0 {
		t.Errorf("a rejected webhook must not create deployments, got %d", len(deployments))
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	repository, _ := store.NewJSONStore(t.TempDir() + "/state.json")
	if _, err := repository.CreateProject(domain.CreateProjectInput{
		Name: "my-app", RepoURL: "https://github.com/example/my-app.git", Branch: "main",
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	server := webhookServer(t, "topsecret", nil)

	response := signedRequest(t, server, "wrongsecret", "push", pushPayload("refs/heads/main", "abc123"))
	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a bad signature", response.StatusCode)
	}

	// Missing header entirely.
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/webhooks/github", strings.NewReader("{}"))
	response2, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response2.Body.Close()
	if response2.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when the signature header is absent", response2.StatusCode)
	}
}

func TestWebhookIgnoresNonPushEvents(t *testing.T) {
	server := webhookServer(t, "topsecret", nil)
	response := signedRequest(t, server, "topsecret", "ping", map[string]any{"zen": "hello"})
	if response.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 for a ping event", response.StatusCode)
	}
}

func TestWebhookDisabledWhenNoSecretConfigured(t *testing.T) {
	server := webhookServer(t, "", nil)
	response := signedRequest(t, server, "", "push", pushPayload("refs/heads/main", "abc123"))
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when webhooks are unconfigured", response.StatusCode)
	}
}

func signedDelivery(t *testing.T, server *httptest.Server, secret string, delivery string, payload any) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(encoded)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/webhooks/github", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Event", "push")
	request.Header.Set("X-Hub-Signature-256", signature)
	if delivery != "" {
		request.Header.Set("X-GitHub-Delivery", delivery)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("post webhook: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestWebhookDeduplicatesRedeliveredPush(t *testing.T) {
	repository, err := store.NewJSONStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	project, err := repository.CreateProject(domain.CreateProjectInput{
		Name: "my-app", RepoURL: "https://github.com/example/my-app.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	registry := &metrics.Registry{}
	server := httptest.NewServer(NewServer(Dependencies{
		Store: repository, Registry: registry, WebhookSecret: "topsecret",
	}))
	defer server.Close()

	payload := pushPayload("refs/heads/main", "abc123def456")
	first := signedDelivery(t, server, "topsecret", "delivery-1", payload)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first delivery status = %d, want 202", first.StatusCode)
	}
	second := signedDelivery(t, server, "topsecret", "delivery-1", payload)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("redelivery status = %d, want 200 with no new deployment", second.StatusCode)
	}

	deployments, err := repository.ListDeployments(project.ID)
	if err != nil {
		t.Fatalf("list deployments: %v", err)
	}
	if len(deployments) != 1 {
		t.Errorf("a redelivered push must not create a second deployment, got %d", len(deployments))
	}
}

func TestWebhookIgnoresPushForAnotherRepository(t *testing.T) {
	repository, err := store.NewJSONStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	project, err := repository.CreateProject(domain.CreateProjectInput{
		Name: "other-app", RepoURL: "https://github.com/example/other-app.git", Branch: "main",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	registry := &metrics.Registry{}
	server := httptest.NewServer(NewServer(Dependencies{
		Store: repository, Registry: registry, WebhookSecret: "topsecret",
	}))
	defer server.Close()

	// The payload is for example/my-app on main; the project tracks
	// example/other-app on main. Same branch, different repo: no deploy.
	response := signedRequest(t, server, "topsecret", "push", pushPayload("refs/heads/main", "abc123"))
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", response.StatusCode)
	}
	deployments, _ := repository.ListDeployments(project.ID)
	if len(deployments) != 0 {
		t.Errorf("a push for another repository must not deploy, got %d deployment(s)", len(deployments))
	}
}

func TestStaticDashboardIsServed(t *testing.T) {
	server := newTestServer(t, nil)
	response, err := server.Client().Get(server.URL + "/")
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
}

func authTestServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	repository, err := store.NewJSONStore(t.TempDir() + "/state.json")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	registry := &metrics.Registry{}
	server := httptest.NewServer(NewServer(Dependencies{
		Store:     repository,
		Registry:  registry,
		AuthToken: token,
	}))
	t.Cleanup(server.Close)
	return server
}

func getWithToken(t *testing.T, server *httptest.Server, path string, token string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestAuthMiddlewareProtectsTheAPI(t *testing.T) {
	server := authTestServer(t, "s3cr3t")

	if response := getWithToken(t, server, "/api/projects", ""); response.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", response.StatusCode)
	}

	if response := getWithToken(t, server, "/api/projects", "wrong"); response.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token status = %d, want 401", response.StatusCode)
	}

	if response := getWithToken(t, server, "/api/projects", "s3cr3t"); response.StatusCode != http.StatusOK {
		t.Errorf("authenticated status = %d, want 200", response.StatusCode)
	}
}

func TestAuthMiddlewareLeavesHealthOpen(t *testing.T) {
	server := authTestServer(t, "s3cr3t")

	if response := getWithToken(t, server, "/api/health", ""); response.StatusCode != http.StatusOK {
		t.Errorf("health status = %d, want 200 without a token", response.StatusCode)
	}
}

func TestAuthMiddlewareAcceptsQueryTokenForEventSource(t *testing.T) {
	server := authTestServer(t, "s3cr3t")

	response, err := server.Client().Get(server.URL + "/api/projects?access_token=s3cr3t")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("query token status = %d, want 200", response.StatusCode)
	}
}

func TestAuthDisabledWhenNoTokenConfigured(t *testing.T) {
	server := authTestServer(t, "")

	if response := getWithToken(t, server, "/api/projects", ""); response.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 when auth is unconfigured", response.StatusCode)
	}
}
