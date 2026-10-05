package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"deploy-platform/internal/webhook"
)

// githubPush turns a GitHub push webhook into queued deployments.
//
// It is the only way a deployment starts without a human clicking deploy, so the
// signature check is not optional: an unsigned push would let anyone who knows
// the URL take over the deployment pipeline.
func (s *Server) githubPush(w http.ResponseWriter, r *http.Request) {
	if s.webhookSecret == "" {
		writeError(w, http.StatusNotFound, fmt.Errorf("webhooks are not configured"))
		return
	}

	// The body is needed both for HMAC verification and for parsing, so it is
	// read once, bounded. GitHub payloads are small.
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("read request body"))
		return
	}

	if err := webhook.VerifySignature(s.webhookSecret, body, r.Header.Get("X-Hub-Signature-256")); err != nil {
		writeError(w, http.StatusUnauthorized, fmt.Errorf("signature verification failed"))
		return
	}

	if event := r.Header.Get("X-GitHub-Event"); event != "push" {
		// A valid signature on an event we do not handle is not an error; it
		// just means there is nothing to do.
		writeJSON(w, http.StatusOK, map[string]any{"status": "ignored", "event": event})
		return
	}

	// GitHub redelivers pushes on timeouts and retries, so deduplicate on the
	// delivery ID: the same push must never create two deployments.
	if delivery := r.Header.Get("X-GitHub-Delivery"); delivery != "" && !s.markDelivery(delivery) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "duplicate", "delivery": delivery})
		return
	}

	var push webhook.PushEvent
	if err := json.Unmarshal(body, &push); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid push payload"))
		return
	}

	projects, err := s.store.ListProjects()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	type result struct {
		ProjectID    string `json:"projectId"`
		Name         string `json:"name"`
		DeploymentID string `json:"deploymentId"`
	}

	var queued []result
	for _, project := range projects {
		if !webhook.ShouldTrigger(project, push) {
			continue
		}
		deployment, err := s.store.CreateDeployment(project.ID, "webhook", "")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		// Record the commit so the history shows what triggered the deploy.
		if push.After != "" {
			deployment.CommitSHA = push.After
			if err := s.store.UpdateDeployment(deployment); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		message := fmt.Sprintf("Deployment queued by push to %s", push.Ref)
		if push.HeadCommit.ID != "" {
			message = fmt.Sprintf("Deployment queued by push %s", push.HeadCommit.ID)
		}
		if _, err := s.store.AddLog(deployment.ID, "system", message); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		queued = append(queued, result{
			ProjectID:    project.ID,
			Name:         project.Name,
			DeploymentID: deployment.ID,
		})
	}

	if queued == nil {
		queued = []result{}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":      "queued",
		"commit":      push.After,
		"deployments": queued,
	})
}

// markDelivery records a GitHub delivery ID and reports whether it is new.
// The set is bounded: once it holds maxDeliveries IDs it is cleared, trading
// a small redelivery window for a hard memory ceiling.
func (s *Server) markDelivery(delivery string) bool {
	const maxDeliveries = 10000
	s.deliveriesMu.Lock()
	defer s.deliveriesMu.Unlock()
	if _, seen := s.deliveries[delivery]; seen {
		return false
	}
	if len(s.deliveries) >= maxDeliveries {
		s.deliveries = make(map[string]struct{})
	}
	s.deliveries[delivery] = struct{}{}
	return true
}
