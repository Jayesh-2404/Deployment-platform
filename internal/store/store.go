package store

import "deploy-platform/internal/domain"

type Store interface {
	CreateProject(input domain.CreateProjectInput) (domain.Project, error)
	ListProjects() ([]domain.Project, error)
	GetProject(id string) (domain.Project, error)
	UpdateProject(project domain.Project) error

	CreateDeployment(projectID string, triggeredBy string, rollbackTo string) (domain.Deployment, error)
	ListDeployments(projectID string) ([]domain.Deployment, error)
	GetDeployment(id string) (domain.Deployment, error)
	UpdateDeployment(deployment domain.Deployment) error
	FindPreviousSuccessfulDeployment(projectID string, currentDeploymentID string) (domain.Deployment, error)

	AddLog(deploymentID string, stream string, message string) (domain.DeploymentLog, error)
	ListLogs(deploymentID string) ([]domain.DeploymentLog, error)
	SubscribeLogs(deploymentID string) (<-chan domain.DeploymentLog, func())
}
