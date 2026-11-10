package deployment

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"launchpad/api/internal/project"
)

// Runtime runs built images as containers and routes traffic to them.
//
// How a request reaches an app:
//
//	browser -> http://hello.localhost -> Traefik (port 80)
//	        -> reads routes/project-7.yml: "hello.localhost goes to lp-hello-42:3000"
//	        -> container lp-hello-42 on the "launchpad-apps" Docker network
//
// The API OWNS the routing table: it writes one small YAML file per project
// into RoutesDir, and Traefik watches that folder and reloads on change.
// Switching versions = rewriting one file, which takes effect in milliseconds.
type Runtime struct {
	runner        Runner
	Network       string        // Docker network shared by Traefik and every app
	RoutesDir     string        // folder Traefik watches for route files
	Domain        string        // apps are served at <subdomain>.<Domain>
	Memory        string        // memory limit per app, e.g. "256m"
	CPUs          string        // CPU limit per app, e.g. "0.5"
	HealthTimeout time.Duration // how long a new container may take to answer
	DrainDelay    time.Duration // wait after switching routes before stopping the old container
	httpClient    *http.Client
}

func NewRuntime(runner Runner, network, routesDir, domain string) *Runtime {
	return &Runtime{
		runner:        runner,
		Network:       network,
		RoutesDir:     routesDir,
		Domain:        domain,
		Memory:        "256m",
		CPUs:          "0.5",
		HealthTimeout: 60 * time.Second,
		DrainDelay:    3 * time.Second,
		httpClient:    &http.Client{Timeout: 2 * time.Second},
	}
}

// Labels on every container we start, so we can always find our own
// containers again (and never touch anything else running in Docker).
const (
	labelManaged    = "launchpad.managed"
	labelProject    = "launchpad.project"
	labelDeployment = "launchpad.deployment"
)

// AppPort is the port the app listens on inside its container: the
// project's PORT env var if set, otherwise 3000.
func AppPort(p project.Project) int {
	if n, err := strconv.Atoi(p.EnvVars["PORT"]); err == nil && n > 0 && n < 65536 {
		return n
	}
	return 3000
}

// AppURL is the address the app is served at.
func (rt *Runtime) AppURL(p project.Project) string {
	return fmt.Sprintf("http://%s.%s", p.Subdomain, rt.Domain)
}

// ContainerName is unique per deployment, so old and new versions can run
// side by side during a switch-over.
func ContainerName(p project.Project, deploymentID int64) string {
	return fmt.Sprintf("lp-%s-%d", p.Subdomain, deploymentID)
}

// Start runs the image as a new container and returns the host port it was
// published on (used only for the health check).
func (rt *Runtime) Start(ctx context.Context, p project.Project, deploymentID int64, image string) (hostPort string, err error) {
	name := ContainerName(p, deploymentID)
	port := AppPort(p)

	// Remove a leftover container with the same name (e.g. from a crash).
	_, _ = rt.runner.Output(ctx, "", "docker", "rm", "-f", name)

	args := []string{
		"run", "--detach",
		"--name", name,
		"--network", rt.Network, // Traefik reaches it by name on this network
		"--label", labelManaged + "=true",
		"--label", fmt.Sprintf("%s=%d", labelProject, p.ID),
		"--label", fmt.Sprintf("%s=%d", labelDeployment, deploymentID),
		// Limits: one app cannot starve the machine or the other apps.
		"--memory", rt.Memory,
		"--cpus", rt.CPUs,
		"--pids-limit", "256",
		"--security-opt", "no-new-privileges",
		// Come back up if the app crashes or Docker restarts.
		"--restart", "unless-stopped",
		// Publish the app port on a random port of 127.0.0.1 (never the
		// public network) so the API can health-check it directly.
		"--publish", fmt.Sprintf("127.0.0.1::%d", port),
		"--env", fmt.Sprintf("PORT=%d", port),
	}
	for k, v := range p.EnvVars {
		if k != "PORT" {
			args = append(args, "--env", k+"="+v) // passed as arguments, never through a shell
		}
	}
	args = append(args, image)

	if _, err := rt.runner.Output(ctx, "", "docker", args...); err != nil {
		return "", fmt.Errorf("docker run: %w", err)
	}

	// "docker port lp-hello-42 3000" prints e.g. "127.0.0.1:55012".
	out, err := rt.runner.Output(ctx, "", "docker", "port", name, strconv.Itoa(port))
	if err != nil {
		return "", fmt.Errorf("find published port: %w", err)
	}
	line := strings.SplitN(out, "\n", 2)[0]
	i := strings.LastIndex(line, ":")
	if i < 0 {
		return "", fmt.Errorf("unexpected docker port output %q", out)
	}
	return line[i+1:], nil
}

// ErrUnhealthy means the new container never answered or exited.
var ErrUnhealthy = errors.New("app did not become healthy")

// WaitHealthy polls the app until it answers HTTP with any status below 500,
// the container exits, or HealthTimeout passes.
//
// WHY: the route must only switch to a container that actually works. If the
// new version crashes on start-up, the old one keeps serving.
func (rt *Runtime) WaitHealthy(ctx context.Context, name, hostPort string, logf func(string)) error {
	ctx, cancel := context.WithTimeout(ctx, rt.HealthTimeout)
	defer cancel()

	url := "http://127.0.0.1:" + hostPort + "/"
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	for attempt := 1; ; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if res, err := rt.httpClient.Do(req); err == nil {
			res.Body.Close()
			if res.StatusCode < 500 {
				logf(fmt.Sprintf("→ Health check passed (HTTP %d after %d attempt(s))", res.StatusCode, attempt))
				return nil
			}
		}

		// Stop waiting at once if the container exited or is crash-looping
		// (--restart restarts it, so RestartCount > 0 means it crashed).
		state, _ := rt.runner.Output(ctx, "", "docker", "inspect", "-f", "{{.State.Running}} {{.RestartCount}}", name)
		if running, restarts, ok := strings.Cut(state, " "); ok && (running == "false" || restarts != "0") {
			return fmt.Errorf("%w: the container exited", ErrUnhealthy)
		}

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%w within %s", ErrUnhealthy, rt.HealthTimeout)
			}
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// ContainerLogs copies the last lines a container printed into the build log,
// so a crash on start-up is visible in the dashboard.
func (rt *Runtime) ContainerLogs(ctx context.Context, name string, logf func(string)) {
	logf("→ Last output from the app:")
	_ = rt.runner.Run(ctx, "", logf, "docker", "logs", "--tail", "30", name)
}

// routeFile is the per-project file Traefik reads, e.g. routes/project-7.yml.
func (rt *Runtime) routeFile(projectID int64) string {
	return filepath.Join(rt.RoutesDir, fmt.Sprintf("project-%d.yml", projectID))
}

// RouteConfig is the Traefik "file provider" configuration that sends
// <subdomain>.<domain> to one container.
func (rt *Runtime) RouteConfig(p project.Project, container string) string {
	name := fmt.Sprintf("project-%d", p.ID)
	return fmt.Sprintf(`# Written by Launchpad. Do not edit: the next deploy replaces it.
http:
  routers:
    %[1]s:
      rule: "Host(`+"`%[2]s.%[3]s`"+`)"
      entryPoints: [web]
      service: %[1]s
  services:
    %[1]s:
      loadBalancer:
        servers:
          - url: "http://%[4]s:%[5]d"
`, name, p.Subdomain, rt.Domain, container, AppPort(p))
}

// Route points the project's subdomain at the given container.
//
// The file is written to a temporary name and then RENAMED into place.
// A rename is atomic: Traefik sees either the old file or the new one,
// never a half-written file.
func (rt *Runtime) Route(p project.Project, container string) error {
	if err := os.MkdirAll(rt.RoutesDir, 0o755); err != nil {
		return err
	}
	final := rt.routeFile(p.ID)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, []byte(rt.RouteConfig(p, container)), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// StopOthers removes every container of the project except keep.
func (rt *Runtime) StopOthers(ctx context.Context, projectID int64, keep string, logf func(string)) {
	for _, name := range rt.containers(ctx, fmt.Sprintf("%s=%d", labelProject, projectID)) {
		if name == keep {
			continue
		}
		if _, err := rt.runner.Output(ctx, "", "docker", "rm", "-f", name); err == nil {
			logf("→ Removed old container " + name)
		}
	}
}

// Remove deletes one container (used when a new one fails its health check).
func (rt *Runtime) Remove(ctx context.Context, name string) {
	_, _ = rt.runner.Output(ctx, "", "docker", "rm", "-f", name)
}

// RemoveProject stops all of a project's containers and deletes its route.
// Called when a project is deleted.
func (rt *Runtime) RemoveProject(ctx context.Context, projectID int64) {
	rt.StopOthers(ctx, projectID, "", func(string) {})
	_ = os.Remove(rt.routeFile(projectID))
}

// containers lists the names of our containers matching a label filter,
// running or not.
func (rt *Runtime) containers(ctx context.Context, labelFilter string) []string {
	out, err := rt.runner.Output(ctx, "", "docker", "ps", "-a",
		"--filter", "label="+labelFilter, "--format", "{{.Names}}")
	if err != nil || out == "" {
		return nil
	}
	return strings.Fields(out)
}

// Reconcile runs at start-up: it removes any container of ours whose
// deployment is not live (left behind by a crash mid-deploy). It compares
// what IS running with what SHOULD be running and fixes the difference,
// the same idea Kubernetes controllers are built on.
func (rt *Runtime) Reconcile(ctx context.Context, store Store) (removed int) {
	out, err := rt.runner.Output(ctx, "", "docker", "ps", "-a",
		"--filter", "label="+labelManaged+"=true",
		"--format", `{{.Names}} {{.Label "`+labelDeployment+`"}}`)
	if err != nil || out == "" {
		return 0
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		id, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		d, err := store.Get(ctx, id)
		if err == nil && d.Status == StatusLive {
			continue // this one should be running
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			continue // database problem: do not delete anything on a guess
		}
		rt.Remove(ctx, fields[0])
		removed++
	}
	return removed
}
