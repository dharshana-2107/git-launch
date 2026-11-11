# Launchpad: interview cheat sheet

One page to read before every interview. Each answer is short on purpose: say it, then let the interviewer pick what to dig into. The phase study guides have the long versions.

## The 30-second pitch

> "Launchpad is a self-hosted deployment platform, a small Vercel. You connect a GitHub repo with a Dockerfile; every push builds an image, health-checks a new container next to the old one, and switches traffic through Traefik with zero downtime. Build logs stream live over Server-Sent Events, and rollback reuses stored images, so it takes seconds. The backend is Go with Postgres, the dashboard is React with TypeScript."

## The 2-minute walkthrough (draw it while you talk)

```
git push → GitHub webhook (HMAC) → Go API → queue (channel) → worker goroutine
   worker: git clone → docker build (tag = commit) → docker run (limits) → health check
         → write Traefik route (atomic rename) → mark live → drain 3 s → remove old
browser ← SSE log stream ← broker ← every log line (also stored in Postgres)
visitor → hello.localhost → Traefik → container on the launchpad-apps network
```

## Resume bullets (pick 3–4)

- Built **Launchpad**, a self-hosted deployment platform (Go, PostgreSQL, React/TypeScript, Docker, Traefik) that builds GitHub repos into containers and serves each at its own subdomain.
- Implemented **zero-downtime blue-green releases**: new containers are health-checked before traffic is switched by atomically rewriting Traefik routes; a failing release never replaces the live version.
- Streamed **live build logs** to the browser with Server-Sent Events, using a pub/sub broker plus stored, numbered lines so clients resume after reconnects without gaps or duplicates.
- Added **deploy-on-push via GitHub webhooks** with HMAC-SHA256 verification (constant-time comparison) and "newest commit wins" coalescing of rapid pushes; **rollback in seconds** by reusing commit-tagged images.
- Enforced correctness under concurrency with **Postgres partial unique indexes** (one active and one live deployment per project) and a background **worker pool** fed by a Go channel; tested with the race detector and fakes for Git/Docker in **GitHub Actions CI**.

Keep numbers honest: only quote timings you measured on your Mac (e.g. "rollback in ~5 s").

## Questions by topic

### Architecture
| Question | Answer |
| --- | --- |
| Why Go? | Docker, Traefik and Kubernetes are Go; goroutines fit background builds and streaming; one static binary; standard library covers HTTP, JSON, SQL. |
| Why no framework or ORM? | Every query and route is visible and explainable; no hidden N+1 queries. Trade-off: a bit more code. |
| Why is the build not done in the HTTP request? | Builds take minutes; requests would time out and the user would see nothing. The request queues the job and returns 202; a worker builds. |
| How is the code organised? | By feature (`project/`, `deployment/`), with a `Store` interface between handlers and Postgres so handlers are tested with an in-memory fake. |
| Why `git`/`docker` as commands? | No heavy SDKs. They sit behind a `Runner` interface, so the worker is tested with a fake: no Docker needed in CI. |

### Concurrency and data
| Question | Answer |
| --- | --- |
| How does the queue work? | Buffered channel of deployment ids; N worker goroutines receive from it; non-blocking send, 503 when full. In memory, so unfinished jobs are marked failed at start-up. |
| Two Deploy clicks at once? | A partial unique index on `deployments(project_id) WHERE status IN (unfinished)` lets only one insert succeed; the other gets `23505` → 409. A check in Go would be a race. |
| Two users take the same subdomain? | `UNIQUE` constraint; the second insert fails with `23505` → 409. |
| How do you make "new live, old stopped" safe? | One transaction, plus a partial unique index allowing one `live` row per project. |
| Three pushes in a minute? | First deploys; the others set a "pending" flag; when it ends, one more deployment builds the branch head. Newest commit wins. |
| What does `defer tx.Rollback()` do after `Commit`? | Nothing. It guarantees every early return undoes the transaction, without writing Rollback on every path. |
| Goroutine vs thread? | Goroutines are scheduled by the Go runtime onto few OS threads, start with tiny stacks, cost little; threads are kernel-scheduled, ~1 MB stack. |

### Live logs (SSE)
| Question | Answer |
| --- | --- |
| Why SSE, not WebSockets or polling? | One-way server → browser; plain HTTP; `EventSource` reconnects with `Last-Event-ID`. Polling wastes requests and lags; WebSockets add a two-way protocol I don't need. |
| Joining a build halfway? | Subscribe first, then read status and stored lines, send them, then forward live lines, skipping line numbers already sent. Reversed order would lose lines. |
| Connection drops? | Each log event has `id: <line>`; the browser reconnects with `Last-Event-ID`; the server sends only later lines from the database. |
| Slow browser? | Each subscriber has a 256-event buffer; when full the broker drops it instead of blocking the build; it reconnects and catches up from the database. |
| Limitation? | The broker is in memory: one API server only. Several servers need Redis pub/sub or similar. |

### Running apps and routing
| Question | Answer |
| --- | --- |
| How does `hello.localhost` reach the container? | `*.localhost` = 127.0.0.1 → Traefik on :80 → matches `Host` against the route file the API wrote → container by name on a shared Docker network. |
| Zero downtime? | Blue-green: start new next to old, health-check, atomically rewrite the route, drain 3 s, remove old. |
| New version crashes? | Health check fails or sees it exit; its logs go into the build log; it's removed; the route never changed, so the old version keeps serving. |
| Why route files, not Docker labels? | Labels route to a container as soon as it starts, before it's healthy, and Traefik would need the Docker socket (root-level access). |
| Why write a temp file and rename? | Rename is atomic: Traefik never reads a half-written config. |
| How do you protect the machine from one app? | `--memory 256m --cpus 0.5 --pids-limit 256 --security-opt no-new-privileges`, a cap on running apps, apps only reachable through Traefik. |
| Crash mid-deploy? | At start-up: unfinished deployments → failed; `Reconcile` removes our containers whose deployment is not live. Same idea as Kubernetes controllers. |
| Compare with Kubernetes? | Health check ≈ readiness probe; route files ≈ Ingress/Service; limits ≈ resources.limits; Reconcile ≈ controller loop; blue-green ≈ rolling update. Kubernetes does it across many machines. |

### Webhooks, rollback, security
| Question | Answer |
| --- | --- |
| How do you trust a webhook? | `X-Hub-Signature-256` = HMAC-SHA256(secret, raw body). Recompute over the raw bytes, compare with `hmac.Equal` (constant time, no timing leak). |
| Why the raw body? | Decoding and re-encoding JSON can change bytes, so the HMAC would not match. |
| Rollback: how and why fast? | New deployment row reusing an old commit-tagged image; skips clone and build; same health-checked switch. Seconds, not minutes. |
| Why a new row, not flip the old one back? | Full history ("rollback of #13"), and the same code path as every deploy. |
| Env var change? | Processes read env at start, so the live image is restarted with the new values ("config" deployment), no rebuild. |
| Disk filling with images? | Keep the newest 5 images per project (by last use) plus the live one; older ones are deleted and their rollback is disabled. |
| Your API was public through a tunnel? | Middleware only lets tunnel traffic (has `Cf-Connecting-Ip`) reach the webhook path; that path checks its signature. |
| Running untrusted Dockerfiles: safe? | Not fully, and I'd say so. Production needs isolated builds (rootless BuildKit/Kaniko in a VM or sandbox), no secrets in builds, network policies. |

### Testing
| Question | Answer |
| --- | --- |
| How did you test it? | Table-driven Go tests; fakes for the store and for git/docker; real HTTP test servers as "apps"; SSE stream tests; `go test -race`; GitHub Actions on every push. Plus manual end-to-end runs on Postgres and Docker. |
| A bug tests caught? | Go's JSON decoder matches field names case-insensitively, so `subDomain` was accepted as `subdomain`. |

## "What would you do next?" (shows judgement)

1. **Login and per-user projects**, then a **GitHub App** instead of manual webhooks (one install, scoped tokens, private repos).
2. **HTTPS and a real domain** (Traefik + Let's Encrypt, wildcard DNS).
3. **Durable queue** (Redis/SQS) and several API servers with a shared pub/sub for logs.
4. **Isolated builds** and stricter runtime sandboxing (gVisor/Firecracker).
5. **Run on Kubernetes**: Deployments, Ingress and probes replace my runtime code.

## Honest limitations (say them before they ask)

- One machine, no login, HTTP only.
- Queue and log broker are in memory.
- Builds run on the host's Docker: untrusted code is not fully isolated.
- The tunnel address changes on restart; a real server would have a fixed domain.
- The running-app limit is a soft limit (two simultaneous first deploys could both pass).
