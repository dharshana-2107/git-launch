// Command server starts the Launchpad REST API.
//
// Start-up order: load config -> connect to Postgres -> run migrations ->
// register routes -> listen. On Ctrl+C (or SIGTERM from Docker) it stops
// accepting new requests and lets in-flight ones finish ("graceful shutdown").
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"launchpad/api/internal/config"
	"launchpad/api/internal/db"
	"launchpad/api/internal/deployment"
	"launchpad/api/internal/httpx"
	"launchpad/api/internal/project"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// run holds the real start-up logic and returns errors instead of exiting,
// which keeps main tiny and makes every failure path explicit.
func run() error {
	cfg := config.Load()

	// ctx is cancelled when the process receives Ctrl+C or SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	projects := project.NewPostgresStore(pool)
	projectHandler := project.NewHandler(projects)
	projectHandler.Register(mux)

	// Deployments: builds that were running when the server last stopped can
	// never finish, so mark them failed before accepting new ones.
	deployments := deployment.NewPostgresStore(pool)
	if n, err := deployments.FailUnfinished(ctx); err != nil {
		return err
	} else if n > 0 {
		slog.Warn("marked interrupted deployments as failed", "count", n)
	}

	routesDir, err := filepath.Abs(cfg.RoutesDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(routesDir, 0o755); err != nil {
		return fmt.Errorf("create routes folder: %w", err)
	}
	runner := deployment.ExecRunner{}
	apps := deployment.NewRuntime(runner, cfg.AppsNetwork, routesDir, cfg.AppsDomain)
	slog.Info("apps", "url", "http://<subdomain>."+cfg.AppsDomain, "routes", routesDir)

	// Remove containers left over from deployments that never went live.
	if n := apps.Reconcile(ctx, deployments); n > 0 {
		slog.Warn("removed leftover containers", "count", n)
	}
	// Deleting a project also stops its containers and removes its route.
	projectHandler.OnDelete = func(ctx context.Context, projectID int64) {
		apps.RemoveProject(ctx, projectID)
	}

	broker := deployment.NewBroker()
	worker := deployment.NewWorker(deployments, projects, broker, runner, apps)
	worker.KeepImages = cfg.KeepImages
	worker.MaxLiveApps = cfg.MaxLiveApps
	worker.Start(ctx, cfg.Workers) // background goroutines; they stop when ctx is cancelled
	deployment.NewHandler(deployments, worker, broker, apps).Register(mux)

	webhooks := deployment.NewWebhookHandler(cfg.WebhookSecret, projects, worker)
	webhooks.Register(mux)
	if !webhooks.Enabled() {
		slog.Info("github webhooks are off (set WEBHOOK_SECRET to turn them on)")
	}

	// Settings the dashboard needs to know about.
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"webhooksEnabled": webhooks.Enabled(),
			"webhookPath":     "/api/webhooks/github",
		})
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           withRecover(withLogging(tunnelGuard(mux))),
		ReadHeaderTimeout: 10 * time.Second, // guards against slow-header (Slowloris) clients
		// No WriteTimeout on purpose: it would cut off long-lived SSE log streams.

		// Every request's context derives from ctx, so on Ctrl+C open SSE
		// streams end at once instead of holding up the shutdown.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	// ListenAndServe blocks, so it runs in its own goroutine while this one
	// waits for either a shutdown signal or a server error.
	errCh := make(chan error, 1)
	go func() {
		slog.Info("api listening", "port", cfg.Port)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// --- middleware --------------------------------------------------------------
// A middleware wraps a handler to add behaviour to EVERY request, so that code
// is written once instead of in each handler.

// statusRecorder remembers the status code a handler wrote, for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush passes through to the real writer; live log streaming (SSE) in
// phase 2 needs it to push each line immediately.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withLogging logs method, path, status and duration for every request.
func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration", time.Since(start).Round(time.Millisecond))
	})
}

// tunnelGuard: a Cloudflare tunnel makes this API reachable from the
// internet so GitHub can send webhooks. The API has no login yet, so requests
// that arrive through the tunnel may ONLY reach the webhook endpoint, which
// checks its own signature. Cloudflare adds the Cf-Connecting-Ip header to
// every request it forwards; local requests never have it.
func tunnelGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cf-Connecting-Ip") != "" && r.URL.Path != "/api/webhooks/github" {
			httpx.WriteError(w, http.StatusForbidden, "only the webhook endpoint is reachable from outside")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withRecover turns a panic inside a handler into a 500 response instead of
// crashing the whole server.
func withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("panic", "value", p, "path", r.URL.Path)
				httpx.WriteError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
