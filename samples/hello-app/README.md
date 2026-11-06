# hello-app

A tiny Node web app with a Dockerfile, for testing Launchpad.
Push this folder as its **own** public GitHub repo, then create a Launchpad project from that repo.

- Listens on `PORT` (default 3000)
- Set `GREETING` as an environment variable to change the heading
- `GET /health` returns `{"status":"ok"}`
