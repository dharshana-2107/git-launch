# Phase 2 study guide: background builds and live logs

Phase 2 adds the **Deploy** button. Clicking it records a deployment, and a background worker clones the repo, builds a Docker image tagged with the commit, and streams every line of output to the browser while it happens. Phase 3 will run that image at `<subdomain>.localhost`.

Read in this order, about 3–4 hours in total:

1. `api/internal/db/migrations/002_deployments.sql`: the tables and the two constraints
2. `api/internal/deployment/model.go`: statuses, types, the `Store` interface
3. `api/internal/deployment/handler.go`: `create` first, then `events` (the SSE stream)
4. `api/internal/deployment/worker.go`: the queue and `process`
5. `api/internal/deployment/broker.go`: publish/subscribe
6. `api/internal/deployment/runner.go`: running `git` and `docker`
7. `api/cmd/server/main.go`: how it is wired, and the shutdown changes
8. `api/internal/deployment/worker_test.go` + `fakes_test.go`
9. `web/src/components/Deployments.tsx`, then `BuildLogs.tsx`

---

## 1. What happens when you click Deploy

```
Browser                    API (handler)              Worker goroutine            Broker         Postgres
  │ POST /deployments ──────►│ INSERT status=queued ───────────────────────────────────────────────►│
  │◄──── 202 + deployment ───│ queue <- id                                                          │
  │ GET /events (SSE) ──────►│ Subscribe ─────────────────────────────────────────► │               │
  │◄── history + status ─────│                         id := <-queue                │               │
  │                          │                         git clone ──► each line: AppendLog ────────► │
  │◄── event: log ───────────│◄──────────────────────────────────── Publish ────────│               │
  │                          │                         docker build ─► (same, per line)             │
  │◄── event: status built ──│◄──────────────────────────────────── Publish ────────│               │
  │◄── event: done ──────────│ handler returns, stream closes                                       │
```

1. `Deployments.deploy()` calls `POST /api/projects/{id}/deployments`.
2. `Handler.create` inserts a row with status `queued`. The **partial unique index** refuses it if this project already has an unfinished deployment → **409**. A missing project breaks the **foreign key** → **404**.
3. The handler calls `worker.Enqueue(id)`, which puts the id on a **buffered channel** without waiting, and returns **202 Accepted** at once. The build has not started yet.
4. The page selects the new deployment, and `BuildLogs` opens an `EventSource` on `GET /api/deployments/{id}/events`.
5. A worker goroutine, blocked on `<-w.queue`, receives the id and runs `process`:
   - status `cloning` → `git clone --depth 1 --branch <branch> <repo> .` into a fresh temp folder
   - reads the commit SHA and message with `git rev-parse` / `git log`
   - checks for a `Dockerfile` in the root
   - status `building` → `docker build -t launchpad/<subdomain>:<sha> .`
   - status `built`, or `failed` with a reason at whichever step broke
6. Every output line goes through `build.log`: numbered, saved to `deployment_logs`, and published to the broker.
7. The SSE handler forwards each event to the browser. On a final status it sends `done` and returns, and `BuildLogs` closes the stream.

---

## 2. Concepts (expect questions on these)

| Concept | Where | What to say |
| --- | --- | --- |
| **Job queue as a buffered channel** | `worker.go` (`queue chan int64`) | Holds up to 100 ids. The handler sends without blocking (`select` with `default`); full → 503. Workers block on receive until a job arrives. |
| **Worker pool** | `Worker.Start(ctx, n)` | n goroutines read the same channel; Go hands each id to exactly one of them. `BUILD_WORKERS` sets n (default 2). |
| **202 Accepted** | `Handler.create` | "Request received, the work happens later." Builds take minutes; an HTTP request must not wait for them. |
| **Server-Sent Events (SSE)** | `Handler.events`, `BuildLogs.tsx` | One long HTTP response. The server writes `event:` / `data:` blocks and calls `Flush()` so each reaches the browser at once. One-way, plain HTTP, auto-reconnect. |
| **Last-Event-ID** | `events`, `writeEvent` | Each log event has `id: <line>`. After a dropped connection the browser reconnects with that id, and the server sends only later lines. |
| **Publish/subscribe** | `broker.go` | The worker publishes; any number of browser tabs subscribe. The worker never knows who is watching. |
| **Mutex** | `Broker.mu` | The subscriber map is used by HTTP goroutines and worker goroutines at once; the lock stops them corrupting it. |
| **Non-blocking send** | `Broker.Publish` | `select { case ch <- e: default: }`. A slow browser cannot stall the build; it is dropped and reconnects. |
| **context cancellation** | `exec.CommandContext`, `WithTimeout` | A build over 15 minutes, or a server shutdown, cancels the context, and Go kills `git`/`docker`. |
| **defer for cleanup** | `defer os.RemoveAll(dir)` | The temp folder is deleted on every exit path, success or failure. |
| **Partial unique index** | `002_deployments.sql` | A unique index over only the rows matching `WHERE status IN (...)`: one active deployment per project, any number of finished ones. |
| **CHECK constraint** | `status` column | The database refuses any status we did not plan for, even from a bug. |

---

## 3. Design decisions and alternatives

| Decision | Why | Alternative and trade-off |
| --- | --- | --- |
| Builds in background goroutines, not in the request | Requests return in milliseconds; builds take minutes | Build inside the request: the browser times out and a server restart loses it silently |
| Channel as the queue | Built into Go, no extra service | Redis / RabbitMQ / SQS: survives restarts and works across several servers, but is one more thing to run |
| Mark unfinished builds failed at start-up | An in-memory queue is empty after a restart, so those builds can never finish | A durable queue would resume them |
| SSE, not WebSockets | Logs flow one way; SSE is plain HTTP and reconnects by itself | WebSockets: two-way, more code; needed only for something like a web terminal |
| Logs stored in Postgres, one row per line | Late viewers and reconnects replay from the database | Keep logs only in memory: lost on restart and for late viewers. Object storage (S3): better for huge logs |
| Subscribe → status → history → live, with line numbers | No line is missed or shown twice when a viewer joins mid-build | Read history first, then subscribe: lines printed in between are lost |
| Drop slow subscribers | The build never waits for a browser | Block until the browser catches up: one slow tab would stall every build |
| `git` and `docker` as commands (`os/exec`) | No extra Go libraries; the same commands you would type | Docker's Go SDK and go-git: more control, many more dependencies |
| `Runner` interface | Worker logic is tested with a fake: no Docker or network in tests | Test with real Docker: slower, needs Docker in CI |
| Image tag = commit SHA | Every image maps to one commit; phase 4's rollback reuses an old tag | `latest`: cannot tell versions apart, cannot roll back |
| `--depth 1` clone | Fetches one commit, not the whole history | Full clone: slower, more disk |
| `GIT_TERMINAL_PROMPT=0` | A private or missing repo fails at once instead of waiting forever for a username | Without it, the build hangs until the 15-minute timeout |
| Max 5,000 stored lines per build | One noisy build cannot fill the database | No limit: risky |
| No `WriteTimeout` on the server; `BaseContext` tied to shutdown | A write timeout would cut long log streams; open streams must end on Ctrl+C | A `WriteTimeout` suits normal APIs without streaming |

---

## 4. The two race conditions this phase handles

**Two Deploy clicks at once.** Both requests would pass any check in Go before either inserts. The partial unique index lets exactly one `INSERT` succeed; the other gets Postgres error `23505` → `ErrAlreadyActive` → 409. Same lesson as the subdomain in phase 1, applied to a subset of rows.

**Joining a build halfway.** If the handler read the stored lines first and subscribed second, lines printed between those two steps would be in neither. So it subscribes first, reads status and history second, and uses line numbers to skip anything it receives twice.

---

## 5. Interview questions with answers

**Why not build inside the HTTP request?**
A build takes minutes. Browsers, proxies and load balancers time out long before that, the user would see nothing until the end, and a crash would lose the build without a trace. The request records the job and returns 202; a worker does the work and reports progress.

**How does your queue work, and what are its limits?**
A buffered Go channel of deployment ids, read by a pool of worker goroutines. It is fast and simple, but it lives in memory: a restart loses queued jobs (I mark them failed at start-up), and it cannot be shared by several API servers. The next step is a durable queue such as Redis or SQS.

**Why SSE and not WebSockets or polling?**
Logs only flow from server to browser. SSE is plain HTTP, needs no extra library (browsers have `EventSource`), and reconnects automatically with `Last-Event-ID`. Polling adds delay and wasted requests; WebSockets add a two-way protocol I do not need.

**What happens if the browser loses connection halfway?**
`EventSource` reconnects by itself and sends `Last-Event-ID` with the last line number it got. The server sends only lines after it, from the database, then continues live.

**What happens if a browser tab is too slow to keep up?**
Each subscriber has a 256-event buffer. If it fills, the broker closes that subscriber instead of blocking the build. The tab reconnects and catches up from the database.

**How do you stop two deployments of the same project at once?**
A partial unique index on `deployments(project_id) WHERE status IN ('queued','cloning','building')`. The database lets only one active row exist per project, even under simultaneous requests.

**What happens when the server restarts mid-build?**
The worker's context is cancelled, Go kills `git`/`docker`, and the build is marked failed. At start-up, `FailUnfinished` marks anything still unfinished as failed, because an in-memory queue cannot resume it.

**How do you prevent a build from running forever?**
Each build runs under `context.WithTimeout` (15 minutes). `exec.CommandContext` kills the process when the deadline passes, and the deployment fails with "build timed out".

**Running a user's Dockerfile is running untrusted code. Is that safe?**
Not fully, and I would say so. `docker build` can run any command in the Dockerfile, using the host's Docker daemon. Mitigations in production: build in an isolated sandbox (rootless BuildKit or Kaniko in a separate VM or Kubernetes pod), limit CPU, memory, time and network, and never give builds access to secrets. That is phase 5 work.

**How did you test the worker without Docker?**
The worker calls git and docker through a `Runner` interface. Tests pass a fake runner that prints build-like lines and can be told to fail at clone, Dockerfile or build. The tests also run with Go's race detector (`go test -race`).

---

## 6. Exercises

1. **Watch the stream raw.** Click Deploy, then in a terminal: `curl -N localhost:8080/api/deployments/<id>/events`. Read the `id:`, `event:` and `data:` lines. Open it again after the build finishes: what changes?
2. **Test the double click.** Run `curl -X POST localhost:8080/api/projects/<id>/deployments` twice quickly. You should get 202, then 409. Which line of SQL causes the 409?
3. **Kill the server mid-build.** Deploy, press Ctrl+C in the API terminal during the build, restart it. What status does the deployment have and why?
4. **Add a `Cancel` button** (stretch): `POST /api/deployments/{id}/cancel`. Hint: keep a `map[int64]context.CancelFunc` of running builds in the worker, guarded by a mutex.
5. **Explain out loud** the 7 steps in section 1 and the two race conditions in section 4.
