package store

import (
	"path/filepath"
	"testing"

	"deploy-platform/internal/domain"
)

func TestJSONStoreProjectDeploymentAndLogs(t *testing.T) {
	repository, err := NewJSONStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewJSONStore() error = %v", err)
	}

	project, err := repository.CreateProject(domain.CreateProjectInput{
		Name:    "PayApp",
		RepoURL: "https://github.com/Jayesh-2404/PayApp",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if project.Branch != "main" {
		t.Fatalf("expected default branch main, got %q", project.Branch)
	}

	deployment, err := repository.CreateDeployment(project.ID, "manual", "")
	if err != nil {
		t.Fatalf("CreateDeployment() error = %v", err)
	}
	deployment.Status = domain.StatusSuccess
	deployment.ImageTag = "deploy-platform/payapp:test"
	if err := repository.UpdateDeployment(deployment); err != nil {
		t.Fatalf("UpdateDeployment() error = %v", err)
	}

	if _, err := repository.AddLog(deployment.ID, "system", "Deployment marked successful"); err != nil {
		t.Fatalf("AddLog() error = %v", err)
	}

	logs, err := repository.ListLogs(deployment.ID)
	if err != nil {
		t.Fatalf("ListLogs() error = %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}

	previous, err := repository.FindPreviousSuccessfulDeployment(project.ID, "")
	if err != nil {
		t.Fatalf("FindPreviousSuccessfulDeployment() error = %v", err)
	}
	if previous.ID != deployment.ID {
		t.Fatalf("expected previous deployment %q, got %q", deployment.ID, previous.ID)
	}
}
