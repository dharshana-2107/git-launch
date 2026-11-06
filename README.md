# Launchpad

A self-hosted mini Vercel/Render: point it at a GitHub repo with a Dockerfile and it builds the image, runs it, and serves it at `http://<subdomain>.localhost`, with live build logs, deployment history and one-click rollback.

**Status: phase 1 of 5** (projects: create, list, edit, delete).

| Part | Tech |
| --- | --- |
| API | Go 1.22+ (standard library `net/http`, `database/sql` + `lib/pq`) |
| Database | PostgreSQL 16 |
| Dashboard | React 18 + TypeScript + Vite |
| Local services | Docker Compose |

## Run it on a Mac

### 1. Install the tools (once)

```bash
brew install go node          # Go 1.22 or newer, Node 20 or newer
```

Install **Docker Desktop** from docker.com and start it (whale icon in the menu bar).

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

## Try the API with curl

```bash
curl -X POST localhost:8080/api/projects \
  -H 'Content-Type: application/json' \
  -d '{"name":"Hello","repoUrl":"https://github.com/octocat/Hello-World","subdomain":"hello","envVars":{"PORT":"3000"}}'

curl localhost:8080/api/projects
curl localhost:8080/api/projects/1
curl -X DELETE localhost:8080/api/projects/1
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

## Project layout

```
api/
  cmd/server/main.go          start-up, middleware, graceful shutdown
  internal/config/            settings from environment variables
  internal/db/                connection pool + migrations (SQL embedded in the binary)
  internal/httpx/             JSON helpers shared by all handlers
  internal/project/           model, validation, Postgres store, HTTP handlers, tests
web/
  src/api.ts                  every call to the API
  src/pages/                  Projects list, New project, Project details
  src/components/ProjectForm  shared create/edit form
docs/STUDY-PHASE-1.md         how this phase works + interview questions
docker-compose.yml            Postgres
```

## Troubleshooting

| Problem | Fix |
| --- | --- |
| `ping database: ... connection refused` | Docker Desktop not running, or run `docker compose up -d` |
| `port 5432 already in use` | Another Postgres is running: `brew services stop postgresql` or change the port in `docker-compose.yml` and `DATABASE_URL` |
| `address already in use :8080` | Run with another port: `PORT=8081 go run ./cmd/server` and update `web/vite.config.ts` |
| Dashboard shows "Could not load projects" | The API is not running in terminal 1 |
| Start from an empty database | `docker compose down -v` (deletes all data) |
