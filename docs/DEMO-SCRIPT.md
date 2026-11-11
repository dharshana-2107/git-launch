# Demo video script (2–3 minutes)

The goal is to show, in one take, the five things that make this a platform: **deploy from a repo, live logs, live URL, deploy on push, instant rollback**. Practise twice before recording.

## Before recording

- [ ] Docker Desktop running; `docker compose up -d`; `docker compose ps` shows postgres healthy and traefik up
- [ ] API running **with** `WEBHOOK_SECRET` (terminal 1), dashboard running (terminal 2)
- [ ] `cloudflared tunnel --url http://localhost:8080` running, and the hello-app webhook URL updated to the new address; push a dummy commit once to check it goes green
- [ ] hello-app already has **one** live deployment (so the first deploy in the video is a zero-downtime switch, and rollback has something to go back to)
- [ ] In `~/Downloads/hello-app/server.js`, the default greeting is something like `"Hello from Launchpad!"`; no `GREETING` env var on the project
- [ ] Browser: Chrome, zoom 110–125 %, close other tabs, hide bookmarks bar. Tabs: dashboard project page, `http://hello.localhost`
- [ ] Terminal: large font (Cmd + to zoom), `cd ~/Downloads/hello-app`, screen cleared
- [ ] Do Not Disturb on. Recorder: QuickTime (File → New Screen Recording) or Cmd + Shift + 5

## Shots and what to say

| Time | On screen | Say (in your own words) |
| --- | --- | --- |
| 0:00 | Dashboard, project list | "This is Launchpad, a small self-hosted Vercel I built in Go and React. You give it a GitHub repo with a Dockerfile, and it builds, runs and serves it at its own URL." |
| 0:10 | Open Hello app project. Point at repo, subdomain, live banner | "This project is my hello-app repo. It's live at hello.localhost." Click the link, show the page. |
| 0:20 | Back on the project page, click **Deploy** | "When I deploy, the API queues the job and a background worker clones the repo and runs docker build." |
| 0:25 | Build logs streaming | "These logs stream live over Server-Sent Events. Every line is also stored, so if I refresh the page mid-build it replays and continues." Optional: refresh to show it. |
| 0:45 | Logs reach "Starting container", "Health check passed", "Routing" | "The new container starts next to the old one. Only when it answers HTTP does the route switch, so there's no downtime, and a broken version never goes live." |
| 1:00 | Live banner shows the new container name | "Live, and the old container has been removed." |
| 1:05 | Switch to terminal. Edit `server.js` greeting to `"Deployed by git push!"`, then `git commit -am "New greeting" && git push` | "Now the real workflow: I change the code and push." |
| 1:20 | Back to dashboard: a new deployment appears with the **git push** label | "GitHub sent a signed webhook. The API verified its HMAC signature and started the deployment by itself." |
| 1:40 | When live, open `hello.localhost`, refresh | "And the change is live." |
| 1:50 | Project page: click **Roll back** on the previous deployment | "If a release is bad, I roll back. Images are tagged by commit and kept, so there's no rebuild." |
| 2:00 | Rollback logs finish in a few seconds; refresh `hello.localhost` | "A few seconds, and the old version is back." |
| 2:10 | Optional: App logs panel; Traefik dashboard (localhost:8090) | "I can also see what the app prints, and the routing table Traefik is using." |
| 2:20 | GitHub repo page (README diagram) | "The code, architecture and design decisions are on my GitHub. Thanks for watching." |

## Tips

- Talk about **why**, not only what: "only when it answers does traffic move" is the sentence that shows understanding.
- If something fails while recording, keep going if you can explain it ("the health check caught it, so the old version stayed live"): that is a feature.
- Keep it under 3 minutes. Cut dead time (waiting for builds) in iMovie, or say one sentence while it runs.
- Upload as **unlisted** on YouTube (or a Loom link) and put the link at the top of the README and on your resume.
