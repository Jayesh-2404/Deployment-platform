package worker

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"deploy-platform/internal/domain"
	"deploy-platform/internal/executor"
	"deploy-platform/internal/store"
)

type Worker struct {
	store    store.Store
	executor executor.Executor
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func New(repository store.Store, exec executor.Executor) *Worker {
	ctx, cancel := context.WithCancel(context.Background())
	return &Worker{
		store:    repository,
		executor: exec,
		ctx:      ctx,
		cancel:   cancel,
	}
}

func (w *Worker) Start() {
	w.wg.Add(1)
	go w.loop()
}

func (w *Worker) Stop() {
	w.cancel()
	w.wg.Wait()
}

func (w *Worker) loop() {
	defer w.wg.Done()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			w.processOnce()
		}
	}
}

func (w *Worker) processOnce() {
	projects, err := w.store.ListProjects()
	if err != nil {
		log.Printf("list projects: %v", err)
		return
	}

	for _, project := range projects {
		deployments, err := w.store.ListDeployments(project.ID)
		if err != nil {
			log.Printf("list deployments: %v", err)
			continue
		}
		for i := len(deployments) - 1; i >= 0; i-- {
			deployment := deployments[i]
			if deployment.Status == domain.StatusQueued {
				w.runDeployment(project, deployment)
				return
			}
			if deployment.Status == domain.StatusRollback {
				w.runRollback(project, deployment)
				return
			}
		}
	}
}

func (w *Worker) runDeployment(project domain.Project, deployment domain.Deployment) {
	w.log(deployment.ID, "system", "Deployment worker picked up job")
	w.setStatus(&deployment, domain.StatusCloning, "")
	w.log(deployment.ID, "system", "Status changed to cloning")

	w.setStatus(&deployment, domain.StatusBuilding, "")
	imageTag, err := w.executor.Deploy(w.ctx, project, deployment, func(stream string, message string) {
		w.log(deployment.ID, stream, message)
	})
	if err != nil {
		w.fail(&deployment, err)
		return
	}

	w.setStatus(&deployment, domain.StatusDeploying, "")
	deployment.ImageTag = imageTag
	if err := w.store.UpdateDeployment(deployment); err != nil {
		log.Printf("update deployment image: %v", err)
	}
	w.log(deployment.ID, "system", "Candidate deployment is ready")

	w.setStatus(&deployment, domain.StatusHealthCheck, "")
	w.log(deployment.ID, "system", "Final health gate passed")

	now := time.Now().UTC()
	deployment.Status = domain.StatusSuccess
	deployment.FinishedAt = &now
	if err := w.store.UpdateDeployment(deployment); err != nil {
		log.Printf("mark deployment success: %v", err)
		return
	}

	project.ActiveDeployID = deployment.ID
	if err := w.store.UpdateProject(project); err != nil {
		log.Printf("update active deployment: %v", err)
	}
	w.log(deployment.ID, "system", "Deployment marked successful")
}

func (w *Worker) runRollback(project domain.Project, rollback domain.Deployment) {
	w.log(rollback.ID, "system", "Rollback worker picked up job")
	target, err := w.store.GetDeployment(rollback.RollbackTo)
	if err != nil {
		w.fail(&rollback, err)
		return
	}

	if err := w.executor.Rollback(w.ctx, project, target, rollback, func(stream string, message string) {
		w.log(rollback.ID, stream, message)
	}); err != nil {
		w.fail(&rollback, err)
		return
	}

	now := time.Now().UTC()
	rollback.Status = domain.StatusSuccess
	rollback.ImageTag = target.ImageTag
	rollback.CommitSHA = target.CommitSHA
	rollback.FinishedAt = &now
	if err := w.store.UpdateDeployment(rollback); err != nil {
		log.Printf("mark rollback success: %v", err)
		return
	}

	if project.ActiveDeployID != "" {
		active, err := w.store.GetDeployment(project.ActiveDeployID)
		if err == nil {
			active.Status = domain.StatusRolledBack
			active.FinishedAt = &now
			_ = w.store.UpdateDeployment(active)
		}
	}

	project.ActiveDeployID = target.ID
	if err := w.store.UpdateProject(project); err != nil {
		log.Printf("update rollback active deployment: %v", err)
	}
	w.log(rollback.ID, "system", "Rollback marked successful")
}

func (w *Worker) setStatus(deployment *domain.Deployment, status domain.DeploymentStatus, message string) {
	deployment.Status = status
	if err := w.store.UpdateDeployment(*deployment); err != nil {
		log.Printf("update deployment status: %v", err)
	}
	if message != "" {
		w.log(deployment.ID, "system", message)
	}
}

func (w *Worker) fail(deployment *domain.Deployment, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	now := time.Now().UTC()
	deployment.Status = domain.StatusFailed
	deployment.Error = err.Error()
	deployment.FinishedAt = &now
	if updateErr := w.store.UpdateDeployment(*deployment); updateErr != nil {
		log.Printf("mark deployment failed: %v", updateErr)
	}
	w.log(deployment.ID, "system", "Deployment failed: "+err.Error())
}

func (w *Worker) log(deploymentID string, stream string, message string) {
	if _, err := w.store.AddLog(deploymentID, stream, message); err != nil {
		log.Printf("add deployment log: %v", err)
	}
}
