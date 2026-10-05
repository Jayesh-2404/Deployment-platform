// Package webhook verifies and parses inbound GitHub webhook deliveries.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"deploy-platform/internal/domain"
)

// signaturePrefix is the algorithm marker GitHub puts in front of the digest.
const signaturePrefix = "sha256="

// branchRefPrefix is the ref namespace GitHub uses for branches, as opposed to
// tags (refs/tags/) or notes (refs/notes/).
const branchRefPrefix = "refs/heads/"

// VerifySignature reports whether payload was signed by GitHub with the given
// secret. The signature header must have the form "sha256=<hex hmac>".
//
// It returns an error rather than a bool so callers can tell a missing or
// malformed header apart from a genuine mismatch. Error messages never contain
// the secret or either digest.
func VerifySignature(secret string, payload []byte, signature string) error {
	if secret == "" {
		return errors.New("webhook secret is not configured")
	}
	// Some GitHub clients pad the header, so trim before parsing.
	trimmed := strings.TrimSpace(signature)
	if trimmed == "" {
		return errors.New("missing signature header")
	}
	digest, found := strings.CutPrefix(trimmed, signaturePrefix)
	if !found {
		return fmt.Errorf("signature must start with %q", signaturePrefix)
	}
	// hex.DecodeString accepts either case, which GitHub clients are not
	// consistent about.
	provided, err := hex.DecodeString(digest)
	if err != nil {
		return errors.New("signature is not valid hex")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), provided) {
		return errors.New("signature does not match payload")
	}
	return nil
}

// PushEvent is the subset of GitHub's push webhook payload this platform needs.
type PushEvent struct {
	Ref     string `json:"ref"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Deleted bool   `json:"deleted"`
	Repo    struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
	HeadCommit struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"head_commit"`
	Pusher struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"pusher"`
}

// MatchesBranch reports whether the event is a push to exactly the given
// branch. GitHub refs look like "refs/heads/main", so the comparison is
// against "refs/heads/"+branch and tag pushes never match.
//
// A deleted ref, an all-zero `after` sha (GitHub sends that once a branch is
// gone), or an empty branch never match: none of them describe code to deploy.
func (e PushEvent) MatchesBranch(branch string) bool {
	if branch == "" {
		return false
	}
	if e.Deleted {
		return false
	}
	if isZeroSHA(e.After) {
		return false
	}
	return e.Ref == branchRefPrefix+branch
}

// ShouldTrigger reports whether a push event should enqueue a deployment for
// project: the push must be on the project's branch *and* for its repository.
// Branch-only matching would deploy every same-branch project on each push,
// so a push to one repo must never trigger another repo's project.
func ShouldTrigger(project domain.Project, event PushEvent) bool {
	if project.Branch == "" {
		return false
	}
	if !event.MatchesBranch(project.Branch) {
		return false
	}
	return event.MatchesRepo(project.RepoURL)
}

// MatchesRepo reports whether the event's repository is the same repository
// as projectRepoURL. The event's clone_url is preferred and full_name is the
// fallback. An empty project URL matches anything, so projects created before
// repository tracking still deploy; an event with no repository information
// never matches, because unattributable pushes must not deploy.
func (e PushEvent) MatchesRepo(projectRepoURL string) bool {
	if strings.TrimSpace(projectRepoURL) == "" {
		return true
	}
	want := RepoIdentity(projectRepoURL)
	if want == "" {
		return false
	}
	if identity := RepoIdentity(e.Repo.CloneURL); identity != "" {
		return identity == want
	}
	if identity := RepoIdentity(e.Repo.FullName); identity != "" {
		return identity == want
	}
	return false
}

// RepoIdentity normalises a repository reference to "owner/repo" in
// lowercase, accepting either a clone URL or a "owner/repo" shorthand, with
// or without a .git suffix. It returns "" when the value does not identify a
// repository at all.
func RepoIdentity(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	path := value
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return ""
		}
		path = parsed.Path
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	segments := strings.Split(path, "/")
	if len(segments) != 2 || segments[0] == "" || segments[1] == "" {
		return ""
	}
	return strings.ToLower(segments[0] + "/" + segments[1])
}

// isZeroSHA reports whether sha is the all-zero placeholder GitHub uses for a
// ref that points at nothing.
func isZeroSHA(sha string) bool {
	return sha != "" && strings.Trim(sha, "0") == ""
}
