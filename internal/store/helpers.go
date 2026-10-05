package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"deploy-platform/internal/domain"
)

// logBroker fans deployment log lines out to SSE subscribers.
//
// It is shared by both store implementations because the fan-out semantics are
// subtle and must not drift between them.
//
// Channels are never closed by the broker. Closing them would race with an
// in-flight send and panic the whole process. Subscribers are unblocked by the
// request context in the SSE handler instead, and the buffered channel is
// collected once the handler drops its reference. Sends use a non-blocking
// select so one stalled client can never apply backpressure to the worker.
type logBroker struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan domain.DeploymentLog]struct{}
}

func newLogBroker() *logBroker {
	return &logBroker{subscribers: make(map[string]map[chan domain.DeploymentLog]struct{})}
}

const logSubscriberBuffer = 64

func (b *logBroker) subscribe(deploymentID string) (<-chan domain.DeploymentLog, func()) {
	channel := make(chan domain.DeploymentLog, logSubscriberBuffer)

	b.mu.Lock()
	if _, ok := b.subscribers[deploymentID]; !ok {
		b.subscribers[deploymentID] = make(map[chan domain.DeploymentLog]struct{})
	}
	b.subscribers[deploymentID][channel] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			delete(b.subscribers[deploymentID], channel)
		})
	}
	return channel, unsubscribe
}

func (b *logBroker) broadcast(deploymentID string, entry domain.DeploymentLog) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for subscriber := range b.subscribers[deploymentID] {
		select {
		case subscriber <- entry:
		default:
			// Slow consumer: drop rather than stall the worker.
		}
	}
}

// normalizeProjectInput applies the validation and defaults shared by every
// Store implementation so both stores reject the same bad input.
func normalizeProjectInput(input domain.CreateProjectInput) (domain.CreateProjectInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.RepoURL = strings.TrimSpace(input.RepoURL)
	input.Branch = strings.TrimSpace(input.Branch)
	input.HealthCheckPath = strings.TrimSpace(input.HealthCheckPath)
	input.LiveURL = strings.TrimSpace(input.LiveURL)

	if input.Name == "" {
		return input, fmt.Errorf("name is required")
	}
	if input.RepoURL == "" {
		return input, fmt.Errorf("repoUrl is required")
	}
	if err := validateRepoURL(input.RepoURL); err != nil {
		return input, err
	}
	if input.Branch == "" {
		input.Branch = "main"
	} else if err := validateBranch(input.Branch); err != nil {
		return input, err
	}
	if input.HealthCheckPath == "" {
		input.HealthCheckPath = "/"
	} else if err := validateHealthCheckPath(input.HealthCheckPath); err != nil {
		return input, err
	}
	if input.LiveURL == "" {
		input.LiveURL = fmt.Sprintf("https://%s.localhost", domain.Slugify(input.Name))
	}
	if input.Host == "" {
		input.Host = hostFromURL(input.LiveURL)
	}
	return input, nil
}

// hostFromURL extracts the proxy hostname from a live URL, falling back to the
// project slug so a malformed LiveURL can never produce an unroutable host.
func hostFromURL(liveURL string) string {
	parsed, err := url.Parse(liveURL)
	if err == nil && parsed.Hostname() != "" {
		return parsed.Hostname()
	}
	return domain.Slugify(liveURL)
}

// validateRepoURL accepts only public https GitHub URLs.
//
// The URL is handed to `git clone` by the Docker executor, so validation is a
// security boundary, not a nicety: anything that is not an https URL on
// github.com is rejected before it can become a command argument or reach the
// network (SSRF), and the argument vector form of os/exec means no shell is
// ever involved even for accepted URLs.
func validateRepoURL(repoURL string) error {
	parsed, err := url.Parse(repoURL)
	if err != nil {
		return fmt.Errorf("repoUrl must be an https GitHub URL like https://github.com/<owner>/<repo>")
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("repoUrl must be an https GitHub URL like https://github.com/<owner>/<repo>")
	}
	if !strings.EqualFold(parsed.Hostname(), "github.com") {
		return fmt.Errorf("repoUrl must be an https GitHub URL like https://github.com/<owner>/<repo>")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("repoUrl must be an https GitHub URL like https://github.com/<owner>/<repo>")
	}
	segments := strings.FieldsFunc(parsed.EscapedPath(), func(r rune) bool { return r == '/' })
	if len(segments) < 2 || segments[0] == "" || segments[1] == "" {
		return fmt.Errorf("repoUrl must be an https GitHub URL like https://github.com/<owner>/<repo>")
	}
	return nil
}

// validateBranch accepts the branch names git itself accepts in practice:
// ASCII letters, digits and . _ / -, never starting with - / ., never ending
// in / ., and never containing .. or //. Anything else is either a ref-format
// violation or an attempt to smuggle flags (a leading dash) or paths into the
// `git clone --branch` argument.
func validateBranch(branch string) error {
	if len(branch) == 0 || len(branch) > 255 {
		return fmt.Errorf("branch must be 1-255 characters")
	}
	first, last := branch[0], branch[len(branch)-1]
	if first == '-' || first == '/' || first == '.' || last == '/' || last == '.' {
		return fmt.Errorf("branch %q is not a valid branch name", branch)
	}
	if strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.Contains(branch, "@{") {
		return fmt.Errorf("branch %q is not a valid branch name", branch)
	}
	for _, r := range branch {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '/', r == '-', r == '+':
		default:
			return fmt.Errorf("branch %q is not a valid branch name", branch)
		}
	}
	return nil
}

// validateHealthCheckPath keeps the probe a plain absolute path. The worker
// appends it to http://127.0.0.1:<port>, so a value with whitespace, control
// characters or a parent segment could change what is actually requested.
func validateHealthCheckPath(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("healthCheckPath must start with /")
	}
	for _, r := range path {
		if r <= 0x1f || r == 0x7f || r == ' ' {
			return fmt.Errorf("healthCheckPath must not contain whitespace or control characters")
		}
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return fmt.Errorf("healthCheckPath must not contain .. segments")
		}
	}
	return nil
}

func newID(prefix string) string {
	return prefix + "_" + shortID()
}

func shortID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}
