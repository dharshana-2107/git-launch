# Phase 1 study guide: projects (the foundation)

Phase 1 is a REST API in Go plus a React dashboard that stores **projects** (a GitHub repo, a branch, a subdomain and environment variables) in Postgres. Nothing is deployed yet; phases 2–3 add building and running.

Read in this order, about 2–3 hours in total:

1. `api/cmd/server/main.go` — how the program starts and stops
2. `api/internal/project/model.go` — the data types and the `Store` interface
3. `api/internal/project/handler.go` — HTTP request → response
4. `api/internal/project/validate.go` — input rules
5. `api/internal/project/postgres.go` — the SQL
6. `api/internal/db/db.go` + `migrations/001_projects.sql` — schema and migrations
7. `api/internal/project/handler_test.go` — tests with a fake store
8. `web/src/api.ts`, then `pages/` and `components/ProjectForm.tsx`

---

## 1. One request, end to end

What happens when you click **Create project**:

1. `ProjectForm.handleSubmit` stops the browser's default form post, turns the env-var rows into an object and calls `api.createProject`.
2. `api.ts` sends `POST /api/projects` with a JSON body. The browser sends it to Vite on :5173, and Vite's proxy (`vite.config.ts`) forwards it to the Go API on :8080.
3. Go's `http.Server` starts a **new goroutine** for the request and passes it through two middlewares: `withRecover` (turns a panic into a 500) and `withLogging` (logs method, path, status, duration).
4. The router (`http.ServeMux`) matches `POST /api/projects` and calls `Handler.create`.
5. `readInput` decodes the JSON (rejecting unknown fields and bodies over 1 MB), calls `Normalize` (trim, lowercase subdomain, default branch `main`) and `Validate`. Invalid input → **422** with one message per field.
6. `PostgresStore.Create` opens a **transaction**, inserts the project with `RETURNING` (gets the new id and timestamps back in one round trip), inserts each env var, then commits.
7. If Postgres reports error code `23505` (unique violation on `subdomain`), the store returns `ErrSubdomainTaken` → handler returns **409**.
8. Success → **201** with the project as JSON. The form navigates to `/projects/{id}`.
9. Back in the form: a 422/409 response becomes an `ApiError` with `fields`, and each message appears under its input.

If you can explain these nine steps without notes, you understand phase 1.

---

## 2. Go concepts used (expect questions on these)

| Concept | Where | What to say |
| --- | --- | --- |
| **Goroutine** | `main.go` (`go func() { srv.ListenAndServe() }`), and every request | A lightweight thread managed by the Go runtime (starts at a few KB of stack). `net/http` runs each request in its own goroutine, so one slow request does not block others. |
| **Channel + select** | `main.go` (`errCh`, `select`) | A typed pipe between goroutines. `select` waits on several channels at once: here, "server crashed" or "shutdown signal", whichever comes first. |
| **context.Context** | every store method, `signal.NotifyContext` | Carries cancellation and deadlines. If the client disconnects, `r.Context()` is cancelled and the database query stops too. |
| **Interfaces** | `Store` in `model.go` | Satisfied implicitly: any type with those methods is a `Store`. No `implements` keyword. That is how the test fake replaces Postgres. |
| **Errors as values** | everywhere | Functions return `(result, error)`; you check `if err != nil`. `fmt.Errorf("...: %w", err)` wraps an error with context; `errors.Is` / `errors.As` look inside the wrapping. |
| **Sentinel errors** | `ErrNotFound`, `ErrSubdomainTaken` | Fixed error values the handler checks with `errors.Is`, so the HTTP layer never needs to know about SQL. |
| **defer** | `defer rows.Close()`, `defer tx.Rollback()` | Runs when the function returns, on every path. `tx.Rollback()` after a successful `Commit` does nothing, so "defer rollback" is the safe default. |
| **Struct tags** | `json:"repoUrl"` | Tell `encoding/json` the JSON field name. `omitempty` drops empty values. |
| **go:embed** | `db.go` | Compiles the SQL files into the binary, so it runs with no files beside it. |
| **Blank import** | `_ "github.com/lib/pq"` | Runs the driver's `init()`, which registers it with `database/sql`. |
| **Mutex** | test `memStore` | Requests run concurrently, so shared data (the map) needs a lock. |

---

## 3. Design decisions and the alternatives

| Decision | Why | Alternative and its trade-off |
| --- | --- | --- |
| Go standard library router (Go 1.22+ patterns like `GET /api/projects/{id}`) | No dependency; method + path parameters are built in now | Chi / Gin / Echo: more features (route groups, middleware helpers), one more dependency |
| `database/sql` + plain SQL, no ORM | Every query is visible; no hidden extra queries | GORM: less code, but hides SQL and makes N+1 queries easy to write by accident |
| Organised by feature (`project/`) | Everything about projects is in one folder | By layer (`models/`, `handlers/`): fine when small, scattered when large |
| `Store` interface between handler and database | Handlers are tested with an in-memory fake, no database needed | Test against a real Postgres (Testcontainers): slower but also tests the SQL |
| `UNIQUE` constraint on `subdomain` | The database is the only place that can stop two simultaneous requests taking the same name | Checking "does it exist?" in Go first: a **race condition** — both requests can pass the check before either inserts |
| Env vars in their own table | Any number per project; `PRIMARY KEY (project_id, key)` keeps one value per key | A JSONB column: simpler, but no per-key constraints |
| `ON DELETE CASCADE` | Deleting a project removes its env vars automatically | Delete them in Go first: more code, easy to forget |
| Replace all env vars on update | Simple and correct inside a transaction | Diff added/changed/removed keys: less writing, more code |
| Transaction around project + env vars | All-or-nothing: no project saved without its env vars | No transaction: a crash halfway leaves half-saved data |
| 422 for invalid fields, 400 for bad JSON, 409 for conflicts | The client can tell "fix your input" from "malformed request" from "already exists" | Everything as 400: simpler, but the client cannot tell them apart |
| Internal errors logged, client sees only "internal error" | Database details never leak to the outside | Returning `err.Error()`: easier debugging, but leaks internals |
| Migrations run at start-up, SQL embedded | One command starts everything; schema in version control | A separate migrate tool (goose, golang-migrate): more features such as down migrations |
| Graceful shutdown | Requests in progress finish before the process exits | Hard exit: in-flight requests fail; matters when Docker or Kubernetes restarts the app |
| Vite proxy for `/api` | Browser sees one origin, so no CORS configuration | CORS headers on the API: needed when frontend and API are on different domains in production |

---

## 4. Something the tests caught

Go's `encoding/json` matches JSON field names **case-insensitively**. A body with `"subDomain"` is accepted as `subdomain`, even with `DisallowUnknownFields()`. The first version of the test assumed otherwise and failed. Good interview story: "a test I wrote taught me how the decoder actually behaves."

---

## 5. Interview questions with answers

**Why Go for the API?**
Docker, Kubernetes and Traefik are written in Go, and later phases talk to Docker and stream logs, which suits goroutines. It compiles to one static binary, and the standard library covers HTTP, JSON and SQL.

**What is the difference between a goroutine and an OS thread?**
Goroutines are scheduled by the Go runtime onto a small number of OS threads, start with a tiny stack that grows as needed, and are cheap to create (thousands are normal). OS threads are scheduled by the kernel and cost about 1 MB of stack each.

**How does your API handle many requests at once?**
`net/http` starts a goroutine per request. The `*sql.DB` pool is safe for concurrent use and hands each query a connection (max 10 open here). Shared in-memory state needs a mutex; the API itself keeps none.

**What is SQL injection and how do you prevent it?**
Building SQL by joining user text lets input change the query (`'; DROP TABLE projects; --`). I use bind parameters (`$1, $2`), so values travel separately from the SQL text and are never parsed as SQL.

**Why a transaction in Create?**
The project row and its env vars must be saved together. If any env-var insert fails, the deferred `Rollback` undoes the project insert too.

**Two users create the subdomain `demo` at the same moment. What happens?**
Both pass validation, both try to insert, Postgres's `UNIQUE` constraint lets exactly one succeed. The other gets error `23505`, which becomes a 409. Checking in Go first would not be enough, because both checks could run before either insert.

**PUT vs PATCH?**
PUT replaces the whole resource (the client sends every field); PATCH changes only the fields sent. My update is PUT: it replaces all fields and the full env-var set.

**Why 201 and 204?**
201 Created says a new resource exists (the body returns it). 204 No Content says the delete worked and there is nothing to return.

**Why return `[]` instead of `null` for an empty list?**
A nil slice encodes as `null`, which breaks `projects.map(...)` in the frontend. Starting from `[]Project{}` encodes as `[]`.

**What does `useEffect(..., [id])` do?**
Runs the effect after render, and again whenever `id` changes, so moving from one project page to another reloads the data. With `[]` it runs once.

**Why `e.preventDefault()` in the form?**
Without it the browser performs a full-page form submission and reloads, losing React's state. We send the data with `fetch` instead.

**What is missing for production?** (shows judgement)
Login and per-user ownership of projects, encryption of env-var values at rest, rate limiting, CORS for a separate frontend domain, and tests that run the SQL against a real Postgres.

---

## 6. Exercises (do at least two; changing code is how it sticks)

1. **Add a `description` field** (optional, max 200 characters): new migration `002_project_description.sql` (`ALTER TABLE projects ADD COLUMN description TEXT NOT NULL DEFAULT ''`), then `model.go`, `validate.go`, `postgres.go` (columns + scan), `api.ts`, `ProjectForm.tsx`. You will touch every layer once.
2. **Add a search filter**: `GET /api/projects?q=hello` matching the name (`WHERE name ILIKE '%' || $1 || '%'`). Read the value with `r.URL.Query().Get("q")`.
3. **Write a test** that `PUT /api/projects/1` with invalid JSON returns 400.
4. **Break it on purpose**: stop Postgres (`docker compose stop postgres`) while the API runs and create a project. What status code comes back, and what is in the API log? Why does the client not see the real error?
5. **Explain out loud** the nine steps in section 1, from memory.
