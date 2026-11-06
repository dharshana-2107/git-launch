# Launchpad

A self-hosted mini Vercel/Render: point it at a GitHub repo with a Dockerfile and it builds the image, runs it, and serves it at `http://<subdomain>.localhost`, with live build logs, deployment history and one-click rollback.

**Status: phase 2 of 5.** Projects (create, list, edit, delete) and deployments: a Deploy button that clones the repo and builds its Docker image in the background, with build logs streamed live to the browser (Server-Sent Events).

| Part | Tech |
| --- | --- |
| API | Go 1.22+ (standard library `net/http`, `database/sql` + `pgx` driver) |
| Database | PostgreSQL 16 |
| Dashboard | React 18 + TypeScript + Vite |
| Builds | `git` + `docker build`, run by background worker goroutines |
| Live logs | Server-Sent Events (`EventSource` in the browser) |
| Local services | Docker Compose |

## Run it on a Mac

### 1. Install the tools (once)

```bash
brew install go node          # Go 1.22 or newer, Node 20 or newer
```

Install **Docker Desktop** from docker.com and start it (whale icon in the menu bar).
`git` is also needed (`git --version`; macOS offers to install it the first time).

Check:

```bash
go version && node -v && docker --version
```

### 2. Start Postgres

```bash
docker compose up -d          # from the repo root
docker compose ps             # postgres should say "healthy"
```

### 3. Start the API (terminal 1)

```bash
cd api
go mod download               # first time only
go run ./cmd/server
```

You should see `applied migration` and `api listening port=8080`. Check it:

```bash
curl localhost:8080/api/health          # {"status":"ok"}
```

### 4. Start the dashboard (terminal 2)

```bash
cd web
npm install                   # first time only
npm run dev
```

Open http://localhost:5173.

### 5. Run the tests

```bash
cd api && go test ./...
```

## Deploy something

1. Push `samples/hello-app` to its **own** public GitHub repo (see its README).
2. In the dashboard: **New project** → that repo's URL, branch `main`, subdomain `hello`.
3. On the project page click **Deploy** and watch the build logs stream.
4. Check the image exists: `docker images launchpad/hello`.

Phase 3 runs it at http://hello.localhost.

## Try the API with curl

```bash
curl -X POST localhost:8080/api/projects \
  -H 'Content-Type: application/json' \
  -d '{"name":"Hello","repoUrl":"https://github.com/octocat/Hello-World","subdomain":"hello","envVars":{"PORT":"3000"}}'

curl localhost:8080/api/projects
curl localhost:8080/api/projects/1
curl -X DELETE localhost:8080/api/projects/1

curl -X POST localhost:8080/api/projects/1/deployments      # start a build (202)
curl -N localhost:8080/api/deployments/1/events             # watch it live (-N = no buffering)
```

## API

| Method | Path | Success | Errors |
| --- | --- | --- | --- |
| GET | `/api/health` | 200 | |
| GET | `/api/projects` | 200 list | |
| POST | `/api/projects` | 201 project | 400 bad JSON, 409 subdomain taken, 422 invalid fields |
| GET | `/api/projects/{id}` | 200 project with env vars | 400 bad id, 404 |
| PUT | `/api/projects/{id}` | 200 project | 400, 404, 409, 422 |
| DELETE | `/api/projects/{id}` | 204 | 400, 404 |
| POST | `/api/projects/{id}/deployments` | 202 queued deployment | 404 project, 409 one already running, 503 queue full |
| GET | `/api/projects/{id}/deployments` | 200 latest 20 | |
| GET | `/api/deployments/{id}` | 200 deployment | 404 |
| GET | `/api/deployments/{id}/events` | SSE stream: `log`, `status`, `done` events | 404 |

## Project layout

```
api/
  cmd/server/main.go          start-up, middleware, graceful shutdown
  internal/config/            settings from environment variables
  internal/db/                connection pool + migrations (SQL embedded in the binary)
  internal/httpx/             JSON helpers shared by all handlers
  internal/project/           model, validation, Postgres store, HTTP handlers, tests
  internal/deployment/        build worker + queue, git/docker runner, live-log broker, SSE, tests
web/
  src/api.ts                  every call to the API
  src/pages/                  Projects list, New project, Project details
  src/components/ProjectForm  shared create/edit form
  src/components/Deployments  Deploy button + deployment history
  src/components/BuildLogs    live log viewer (EventSource)
samples/hello-app/            tiny Node app with a Dockerfile, to deploy
docs/STUDY-PHASE-1.md         how each phase works + interview questions
docs/STUDY-PHASE-2.md
docker-compose.yml            Postgres
```

## Troubleshooting

| Problem | Fix |
| --- | --- |
| `ping database: ... connection refused` | Docker Desktop not running, or run `docker compose up -d` |
| `port 5432 already in use` | Another Postgres is running: `brew services stop postgresql` or change the port in `docker-compose.yml` and `DATABASE_URL` |
| `address already in use :8080` | Run with another port: `PORT=8081 go run ./cmd/server` and update `web/vite.config.ts` |
| Dashboard shows "Could not load projects" | The API is not running in terminal 1 |
| Build fails at `docker build` with "Cannot connect to the Docker daemon" | Start Docker Desktop |
| Build fails at `git clone` | The repo must be public and the branch must exist (many older repos use `master`) |
| Build fails with "no Dockerfile" | The Dockerfile must be in the repo's root folder |
| Deployment stuck as failed "server restarted" | Expected: builds running when the API stopped are marked failed at start-up |
| Start from an empty database | `docker compose down -v` (deletes all data) |
