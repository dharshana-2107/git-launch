# Phase 3 study guide: running apps at a live URL

Phase 3 turns a built image into a running app at `http://<subdomain>.localhost`. A deploy now starts a container, checks it answers HTTP, switches traffic to it, and removes the previous version, with no moment where the app is down.

Read in this order, about 3 hours in total:

1. `docker-compose.yml`: Traefik and the `launchpad-apps` network
2. `api/internal/db/migrations/003_runtime.sql`: new statuses, one-live-per-project index
3. `api/internal/deployment/runtime.go`: `Start`, `WaitHealthy`, `Route`, `StopOthers`, `Reconcile`
4. `api/internal/deployment/worker.go`: `release` (the switch-over, in order)
5. `api/internal/deployment/postgres.go`: `MarkLive`
6. `api/cmd/server/main.go`: wiring, `Reconcile` at start-up, the `OnDelete` hook
7. `api/internal/deployment/worker_test.go`: the swap and failed-health-check tests
8. `web/src/components/Deployments.tsx`: the live banner

---

## 1. How a browser request reaches an app

```
Browser: http://hello.localhost
   │   *.localhost always means "this machine" (127.0.0.1)
   ▼
Traefik, port 80 (container, started by docker compose)
   │   reads data/routes/project-7.yml:
   │     Host(`hello.localhost`)  →  http://lp-hello-42:3000
   ▼
Container lp-hello-42 (on the Docker network "launchpad-apps")
   │   Docker's built-in DNS turns the name "lp-hello-42" into the container's IP
   ▼
Your app, listening on port 3000 (the PORT env var)
```

- **Traefik** is a **reverse proxy**: one entry point (port 80) that forwards each request to the right container based on the `Host` header. Nginx or Caddy could do the same job.
- **The API owns the routing table.** For each project it writes one small YAML file into `data/routes/`. Traefik watches that folder (`--providers.file.watch`) and reloads in milliseconds.
- **The shared network** lets Traefik reach containers **by name**. No app port is published to the outside: the only public entry is Traefik.

---

## 2. What happens in a deploy (after the build)

`Worker.release` in `worker.go`, in this exact order:

| Step | Code | If it fails |
| --- | --- | --- |
| 1. Status `deploying` | `b.setStatus(StatusDeploying)` | |
| 2. Start the **new** container next to the old one, with limits and env vars | `rt.Start` → `docker run --detach --name lp-hello-43 --network launchpad-apps --memory 256m --cpus 0.5 …` | Container removed, deployment `failed`, old version still live |
| 3. **Health check**: call the app until it answers HTTP below 500 (max 60 s), stop early if the container exits | `rt.WaitHealthy` | Last 30 lines of the app's output copied into the build log, container removed, `failed`, old version still live |
| 4. **Switch the route**: write the new route file to a temp name, then rename it into place | `rt.Route` | `failed`, old version still live |
| 5. Mark live: new → `live`, previous live → `stopped`, in **one transaction** | `MarkLive` | |
| 6. Wait 3 s (**drain**) so Traefik loads the new route and requests to the old container finish | `rt.DrainDelay` | |
| 7. Remove the old container | `rt.StopOthers` | |

The key idea: **the route is the switch, and it only flips after the new version has proved it works.** Before step 4, all traffic goes to the old container; after it, all traffic goes to the new one. That is a **blue-green deployment**: two versions side by side, traffic flipped from one to the other.

---

## 3. Concepts (expect questions on these)

| Concept | Where | What to say |
| --- | --- | --- |
| **Reverse proxy** | Traefik | One public entry point that forwards requests to internal services by host name or path. Also where TLS, rate limiting and load balancing usually live. |
| **Host-based routing** | route file `rule: Host(...)` | Many apps share port 80; the `Host` header decides which one gets the request. |
| **Docker network + DNS** | `--network launchpad-apps` | Containers on one user-defined network reach each other by container name. |
| **Health check** | `WaitHealthy` | Proof the app is actually serving before it gets traffic. "Process started" is not "app ready". |
| **Blue-green / zero downtime** | `release` | New version runs next to the old; traffic flips only when the new one is healthy; old one removed after a drain period. |
| **Atomic rename** | `Route` | Write `project-7.yml.tmp`, then `os.Rename` it to `project-7.yml`. A rename replaces the file in one step, so Traefik never reads a half-written file. |
| **Resource limits** | `--memory`, `--cpus`, `--pids-limit` | One app cannot use all the RAM or CPU, or fork-bomb the machine (pids limit). |
| **Labels** | `launchpad.managed`, `launchpad.project`, `launchpad.deployment` | Tags on containers so the platform finds exactly its own containers, and never touches others. |
| **Reconciliation** | `Reconcile` at start-up | Compare what IS running with what SHOULD run (the database), and fix the difference. Kubernetes controllers work this way. |
| **Hook / dependency inversion** | `project.Handler.OnDelete` | The project package stays unaware of Docker; `main.go` plugs in "also remove its containers". |

---

## 4. Design decisions and alternatives

| Decision | Why | Alternative and trade-off |
| --- | --- | --- |
| Traefik with **route files** written by the API | Explicit: the platform decides when traffic moves; the file is easy to read and debug | Traefik's **Docker provider** (routes from container labels): less code, but a container gets traffic as soon as it starts, before it is healthy, and Traefik needs the Docker socket |
| No Docker socket in Traefik | The socket gives full control of Docker (effectively root on the host); the proxy does not need it | Mount it to use the Docker provider |
| Health check through a port published on `127.0.0.1` only | The API runs on your Mac and cannot reach container IPs directly there; `127.0.0.1` keeps it private | Health check inside the network (a probe container) or through Traefik with a preview route |
| "Healthy" = any HTTP status below 500 | Any real web app answers `/` somehow (even 404 means it is running) | A `/health` path per project: more precise, one more setting |
| One container per deployment, named `lp-<subdomain>-<id>` | Old and new can run side by side during the switch | Reuse one name: forces stop-then-start, which means downtime |
| 3 s drain before removing the old container | Requests already inside the old container can finish | Remove at once: a few requests may fail; watch connection counts: more complex |
| `MarkLive` in one transaction + partial unique index on `live` | Never two live deployments, never zero because of a crash halfway | Two separate updates: a crash between them leaves the data inconsistent |
| `--restart unless-stopped` | A crashed app or a Docker restart brings it back; route files are on disk, so routing comes back too | No restart policy: an app crash means downtime until the next deploy |
| Env vars passed as `--env KEY=VALUE` arguments | No shell involved, so values cannot inject commands | Values are visible in `docker inspect`; a secrets store would hide them (production) |
| Route file named by project id (`project-7.yml`) | Changing the subdomain does not leave an old file behind; it applies on the next deploy | Name by subdomain: a renamed project leaves a stale route |
| `Reconcile` at start-up | A crash mid-deploy can leave a container with no live deployment; this removes it | Leave it: orphan containers waste memory forever |

---

## 5. Interview questions with answers

**How does a request to `hello.localhost` reach the right container?**
`*.localhost` resolves to 127.0.0.1, so it hits Traefik on port 80. Traefik matches the `Host` header against the route files the API wrote, and forwards to the container by name over a shared Docker network, where Docker's DNS resolves the name.

**How do you deploy with zero downtime?**
Blue-green. The new container starts next to the old one; it gets traffic only after passing a health check; then I switch the route with an atomic file rename, wait a few seconds for requests to drain, and remove the old container. Until the switch, the old version serves everything.

**What happens if the new version crashes on start-up?**
The health check fails (or sees the container exit), I copy the app's last log lines into the build log, remove the new container, and mark the deployment failed. The route was never changed, so the old version keeps serving. There is a test for exactly this (`TestFailedHealthCheckKeepsOldVersion`).

**Why not let Traefik discover containers through Docker labels?**
It would route to a container the moment it starts, before the app is ready, so users could hit a half-started app. Writing the routes myself means traffic moves only after the health check. It also means Traefik needs no access to the Docker socket, which is a security win.

**Why is the route written to a temp file and renamed?**
A rename replaces the file atomically. If I wrote the file in place, Traefik could read it halfway through writing and load a broken config.

**How do you stop one app from taking down the others?**
Per-container limits: 256 MB memory, half a CPU, at most 256 processes, and `no-new-privileges`. Apps also never publish ports publicly; only Traefik is exposed.

**What happens if the API crashes in the middle of a deploy?**
At start-up, unfinished deployments are marked failed, and `Reconcile` removes any of our containers whose deployment is not live. The previous live version is untouched, because the route only changes at the very end.

**What happens if the Mac restarts?**
Docker restarts the live containers (`--restart unless-stopped`) and Traefik reloads the route files from disk, so the apps come back without the API doing anything.

**How does this differ from Kubernetes?**
Kubernetes does the same jobs with more machinery: a Deployment does rolling updates with readiness probes (my health check), a Service and Ingress do routing (my Traefik routes), resource requests and limits match my `--memory`/`--cpus`, and controllers reconcile desired against actual state (my `Reconcile`). My version runs on one machine; Kubernetes does it across many and reschedules containers when a machine dies.

**What is still missing for production?**
HTTPS (Traefik can get Let's Encrypt certificates), a real domain with wildcard DNS, build and run isolation (separate VM or gVisor, because a user's code runs on my machine), secrets not visible in `docker inspect`, and more than one machine.

---

## 6. Exercises

1. **Watch the switch-over.** Deploy hello-app, then in a terminal run `while true; do curl -s hello.localhost | grep -o 'lp-[a-z]*-[0-9]*\|Hello[^<]*'; sleep 0.3; done`. Change the `GREETING` env var, redeploy, and watch the output change from old to new with no errors.
2. **Break the app on purpose.** In your hello-app repo, change `server.js` to `process.exit(1)` at the top, push, and deploy. Read the build log: what failed, and is the old version still live at `hello.localhost`?
3. **Read the routing table.** Open `data/routes/project-<id>.yml` and the Traefik dashboard at http://localhost:8090. Find your router and the container it points at.
4. **Find your containers.** `docker ps --filter label=launchpad.managed=true` lists every app. Kill one with `docker rm -f <name>`; what happens at `hello.localhost`? Restart the API: what does `Reconcile` do, and why does it not bring the app back? (Hint: it only removes extras; recreating missing ones is a good stretch exercise.)
5. **Explain out loud** the 7 steps of section 2, and why the route is the last thing to change.
