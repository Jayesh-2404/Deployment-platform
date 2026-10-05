// Package proxy owns per-project reverse proxy routing for a single VPS.
//
// The platform needs one stable HTTPS hostname per deployed project, plus a
// hostname for its own dashboard. Caddy is the proxy: it terminates TLS
// automatically for every site address it is given, so the platform only has
// to render a Caddyfile. There is no Caddy Go library here on purpose - the
// config is rendered as text and the `caddy` binary is shelled out to.
//
// The whole package is built around a canonical file. Every render produces
// the same bytes for the same routes, so a deploy that changes nothing writes
// nothing meaningful and operators diffing the file always see the real change
// rather than reshuffled blocks.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Route maps one public hostname to a container on loopback. Dashboard routes
// the control plane itself; project routes point at a deployed container.
type Route struct {
	Host      string
	Upstream  string // "127.0.0.1:32768"
	Dashboard bool
}

// Validate reports the first problem with the route: an empty host, a host
// that is not a plain DNS name, an empty upstream, or an upstream that is not
// host:port. Host is checked before upstream so a route that is wrong in both
// ways names the host first - that is the half the caller usually got wrong.
func (r Route) Validate() error {
	if strings.TrimSpace(r.Host) == "" {
		return errors.New("host is required")
	}
	if !isHostname(r.Host) {
		return fmt.Errorf("host %q is not a valid hostname", r.Host)
	}
	if strings.TrimSpace(r.Upstream) == "" {
		return errors.New("upstream is required")
	}
	if !isHostPort(r.Upstream) {
		return fmt.Errorf("upstream %q must be host:port", r.Upstream)
	}
	return nil
}

// isHostname accepts the shape of host a project can actually be served on:
// letters, digits, dots and hyphens, no leading or trailing dot and no empty
// label. Underscores and any other character are rejected because Caddy would
// treat them as part of a site address we cannot issue a certificate for.
func isHostname(host string) bool {
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	if strings.Contains(host, "..") {
		return false
	}
	for i := 0; i < len(host); i++ {
		char := host[i]
		switch {
		case char >= 'a' && char <= 'z':
		case char >= 'A' && char <= 'Z':
		case char >= '0' && char <= '9':
		case char == '.' || char == '-':
		default:
			return false
		}
	}
	return true
}

// isHostPort accepts "host:port" with a numeric port in range. The host part
// is only checked for emptiness because a container is reached on loopback by
// IP here, and an IP has more punctuation than a hostname rule allows.
func isHostPort(upstream string) bool {
	host, port, found := strings.Cut(upstream, ":")
	if !found || host == "" || port == "" {
		return false
	}
	for i := 0; i < len(port); i++ {
		if port[i] < '0' || port[i] > '9' {
			return false
		}
	}
	number := 0
	for i := 0; i < len(port); i++ {
		number = number*10 + int(port[i]-'0')
		if number > 65535 {
			return false
		}
	}
	return number > 0
}

// Config controls Caddyfile rendering and reload behaviour.
type Config struct {
	// BaseDomain is the apex every project host sits under, e.g. "example.com".
	BaseDomain string
	// DashboardHost is the hostname serving the control plane itself.
	DashboardHost string
	// ACMEEmail is the contact address for automatic HTTPS. When empty the
	// global email option is omitted.
	ACMEEmail string
	// CaddyfilePath is the file the rendered config is written to.
	CaddyfilePath string
	// ReloadBinary, when non-empty, is executed after the file is written so
	// the change takes effect. Caddy also auto-reloads on file change, so this
	// is optional; it exists for hosts where the auto-reload is disabled.
	ReloadBinary string // e.g. "caddy"
	// ReloadArgs are the arguments passed to ReloadBinary, e.g.
	// []string{"reload", "--config", "/etc/caddy/Caddyfile"}.
	ReloadArgs []string
}

// Writer applies a set of routes. Callers depend on the interface, not on
// Caddy, so the simulation path can swap in NoopWriter.
type Writer interface {
	Apply(routes []Route) error
}

// NoopWriter satisfies Writer without touching a proxy. Used when routing is
// disabled (local dev / simulation executor) so callers never need a nil check.
type NoopWriter struct{}

func (NoopWriter) Apply([]Route) error { return nil }

// CaddyWriter renders and atomically installs a Caddyfile.
type CaddyWriter struct {
	config Config
	// reload runs the reload command. It is a field rather than a direct call
	// to os/exec so tests can record the invocation without a real caddy
	// binary on the machine.
	reload func(ctx context.Context, binary string, args []string) error
}

var (
	_ Writer = (*CaddyWriter)(nil)
	_ Writer = NoopWriter{}
)

// NewCaddyWriter returns a writer that installs the rendered Caddyfile at
// cfg.CaddyfilePath. The base domain is required because it is the documented
// apex for the deployment, and the path is required because there is nowhere
// to write without it.
func NewCaddyWriter(cfg Config) (*CaddyWriter, error) {
	if strings.TrimSpace(cfg.CaddyfilePath) == "" {
		return nil, errors.New("caddyfile path is required")
	}
	if strings.TrimSpace(cfg.BaseDomain) == "" {
		return nil, errors.New("base domain is required")
	}
	return &CaddyWriter{config: cfg, reload: execReload}, nil
}

// Render produces the complete Caddyfile text. Exported because it is a pure
// function and must be fully unit-testable without touching the filesystem.
//
// Dashboard sites come first, then project sites, each group sorted by host.
// The ordering is by host rather than by input order so an unchanged route set
// renders byte-identical text, which is what lets operators trust a diff of
// the installed file. Any existing file content is ignored: the rendered file
// is the whole truth.
func Render(cfg Config, routes []Route) string {
	ordered := orderedRoutes(routes)

	var builder strings.Builder
	builder.WriteString("# Generated by deploy-platform. Edits are overwritten on the next deploy.\n")
	if cfg.BaseDomain != "" {
		fmt.Fprintf(&builder, "# Base domain: %s\n", cfg.BaseDomain)
	}
	builder.WriteString("{\n")
	if cfg.ACMEEmail != "" {
		fmt.Fprintf(&builder, "\temail %s\n", cfg.ACMEEmail)
	}
	builder.WriteString("}\n")

	for _, route := range ordered {
		fmt.Fprintf(&builder, "\n%s {\n", route.Host)
		fmt.Fprintf(&builder, "\treverse_proxy %s", route.Upstream)
		if route.Dashboard {
			// The dashboard is served by the platform itself, so it needs to
			// know the name and scheme it was reached under to build absolute
			// URLs and set cookies. Project containers are not told about this:
			// they only ever see the host they were routed from, which is the
			// project name.
			builder.WriteString(" {\n")
			builder.WriteString("\t\theader_up Host {host}\n")
			builder.WriteString("\t\theader_up X-Forwarded-Proto https\n")
			builder.WriteString("\t}\n")
		} else {
			builder.WriteString("\n")
		}
		builder.WriteString("}\n")
	}
	return builder.String()
}

// orderedRoutes copies and sorts routes into render order. The caller's slice
// is never touched, so a caller can reuse its route list across calls.
func orderedRoutes(routes []Route) []Route {
	ordered := make([]Route, len(routes))
	copy(ordered, routes)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Dashboard != ordered[j].Dashboard {
			return ordered[i].Dashboard
		}
		return ordered[i].Host < ordered[j].Host
	})
	return ordered
}

// Apply renders the config, validates every route first, writes the file
// atomically, then runs the reload command if one is configured.
//
// Validation happens before any write so a bad route can never leave a
// half-updated proxy on the box. The write goes through a temp file in the
// same directory plus a rename, because Caddy reads the file on every reload
// and a truncate-then-write window would let it parse a half file.
func (w *CaddyWriter) Apply(routes []Route) error {
	if err := validateRoutes(routes); err != nil {
		return err
	}
	if err := writeFileAtomic(w.config.CaddyfilePath, Render(w.config, routes)); err != nil {
		return err
	}
	if w.config.ReloadBinary == "" {
		return nil
	}
	// Apply takes no context because the Writer interface predates the worker's
	// context plumbing; the reload is a short local command, so background is
	// the honest default rather than a nil context.
	return w.reload(context.Background(), w.config.ReloadBinary, w.config.ReloadArgs)
}

// validateRoutes rejects the whole set or nothing. A duplicate host is an
// error because two site blocks for one name makes the rendered file depend on
// block order, which is exactly the drift the canonical render exists to
// prevent. Hosts are compared case-insensitively because DNS does.
func validateRoutes(routes []Route) error {
	seen := make(map[string]struct{}, len(routes))
	for i, route := range routes {
		if err := route.Validate(); err != nil {
			return fmt.Errorf("route %d: %w", i, err)
		}
		key := strings.ToLower(route.Host)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate route host %q", route.Host)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// writeFileAtomic replaces path with content via a temp file in the same
// directory followed by a rename. The parent directory must already exist: the
// platform writes to a path the operator provisioned, and creating it here
// would turn a typo in a config path into a silent mkdir.
func writeFileAtomic(path string, content string) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create temp caddyfile: %w", err)
	}
	// Caddy runs as its own user and only needs to read the file, so the
	// default 0600 from CreateTemp would be too tight to be useful.
	const caddyfileMode = 0o644
	if err := temp.Chmod(caddyfileMode); err != nil {
		temp.Close()
		os.Remove(temp.Name())
		return fmt.Errorf("chmod temp caddyfile: %w", err)
	}
	if _, err := temp.WriteString(content); err != nil {
		temp.Close()
		os.Remove(temp.Name())
		return fmt.Errorf("write temp caddyfile: %w", err)
	}
	if err := temp.Close(); err != nil {
		os.Remove(temp.Name())
		return fmt.Errorf("close temp caddyfile: %w", err)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		os.Remove(temp.Name())
		return fmt.Errorf("install caddyfile: %w", err)
	}
	return nil
}

// execReload runs the reload binary and folds its output into the error,
// because "exit status 1" on its own tells an operator nothing about a
// Caddyfile that did not parse.
func execReload(ctx context.Context, binary string, args []string) error {
	output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	details := strings.TrimSpace(string(output))
	if details == "" {
		return fmt.Errorf("reload command failed: %w", err)
	}
	return fmt.Errorf("reload command failed: %w: %s", err, details)
}
