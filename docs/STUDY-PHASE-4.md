# Phase 4 study guide: deploy on push, rollback, config changes

Phase 4 makes Launchpad behave like Vercel or Render: a `git push` deploys by itself, any earlier version can be restored in seconds without rebuilding, and changing an environment variable restarts the app with it. You can also read what the running app prints.

Read in this order, about 2–3 hours in total:

1. `api/internal/db/migrations/004_triggers.sql`: why a deployment happened, and which one it reused
2. `api/internal/deployment/webhook.go`: GitHub push → signature check → deploy
3. `api/internal/deployment/worker.go`: `Deploy`, `runPending` ("newest commit wins"), `redeploy`
4. `api/internal/deployment/handler.go`: `redeploy` (rollback vs config) and `appLogs`
5. `api/cmd/server/main.go`: `tunnelGuard`, `/api/config`
6. `api/internal/deployment/redeploy_test.go`: every case above, tested
7. `web/src/pages/ProjectPage.tsx` (env change → restart), `components/Deployments.tsx` (Roll back), `AppLogs.tsx`, `AutoDeploy.tsx`

---

## 1. Deploy on git push

```
git push ──► GitHub ──POST (signed)──► https://xyz.trycloudflare.com/api/webhooks/github
                                             │  Cloudflare tunnel (cloudflared on your Mac)
                                             ▼
                                      tunnelGuard: only this path is allowed from outside
                                             ▼
                                      WebhookHandler.github
                                        1. read the RAW body
                                        2. check X-Hub-Signature-256 (HMAC-SHA256 with WEBHOOK_SECRET)
                                        3. event "push"? branch push? (not a tag, not a delete)
                                        4. find projects with this repo + branch
                                        5. worker.Deploy(..., Trigger: push) for each
                                        6. answer 202 within milliseconds
```

**Why the tunnel:** GitHub is on the internet; your API is on `localhost`. `cloudflared tunnel --url http://localhost:8080` gives you a temporary public HTTPS address that forwards to your Mac. No account needed.

**Why `tunnelGuard`:** the tunnel makes the *whole* API reachable from the internet, and the API has no login. Cloudflare adds a `Cf-Connecting-Ip` header to every request it forwards, so the guard lets those requests reach only the webhook endpoint (which checks its own signature) and answers 403 to everything else.

---

## 2. The signature check (the most important security code in this phase)

GitHub and your API share a secret (`WEBHOOK_SECRET`). For every delivery GitHub computes:

```
X-Hub-Signature-256: sha256=<hex of HMAC-SHA256(secret, raw request body)>
```

`ValidSignature` computes the same HMAC over the bytes it received and compares.

- **HMAC** proves two things: the sender knows the secret, and the body was not changed on the way. Anyone can call the URL; only GitHub can produce a valid signature.
- **Verify the raw bytes before decoding.** Decoding the JSON and re-encoding it could change spacing or key order, and then the HMAC would not match.
- **`hmac.Equal`, not `==`.** `==` stops at the first different byte, so the time it takes leaks how many leading bytes were right. An attacker could use that to guess a signature byte by byte (a **timing attack**). `hmac.Equal` always takes the same time.
- **Respond fast.** GitHub waits about 10 seconds and then marks the delivery as failed, so the handler only queues work and returns 202.

---

## 3. "Newest commit wins"

Three pushes in a row while a build is running should not cause three more builds.

- `Worker.Deploy` tries to create the deployment. The database refuses a second unfinished one (`ErrAlreadyActive`, the partial unique index from phase 2).
- For a **push**, instead of failing, the worker sets `pending[projectID] = true` and returns `ErrDeferred`. More pushes just set the same flag again.
- When the running deployment ends, the worker loop calls `runPending`, which clears the flag and starts **one** new deployment. It clones the branch's newest commit, so the skipped pushes are included.

A map plus a mutex is enough because the flag only needs to say "something is waiting", not what.

---

## 4. Rollback and config restart: the same mechanism

`POST /api/deployments/{id}/redeploy` creates a new deployment that **copies the image tag and commit** of deployment `{id}` (`NewDeployment.Source`). The worker sees `Redeploy()` is true and skips clone and build:

```
redeploy: check the image still exists (docker image inspect)  →  release() exactly as in phase 3
```

| You redeploy… | Trigger | What it means |
| --- | --- | --- |
| an older, `stopped` deployment | `rollback` | go back to that version |
| the `live` deployment | `config` | same version, restarted with the project's current env vars |
| a `failed` deployment | refused, 409 | it never proved it works |

- **Why so fast:** images are tagged by commit (`launchpad/hello:4288c55`) and kept, so going back is just "run that image again", still with the health check and zero-downtime switch.
- **Why env vars need a restart:** a process reads its environment once, when it starts. Changing the database row changes nothing in the running container; `ProjectPage` therefore restarts the live image right after you save new env vars.
- **The new deployment is a new row,** pointing back with `source_deployment_id`. History is never rewritten: you can see "rollback of #13" in the list.

---

## 5. Design decisions and alternatives

| Decision | Why | Alternative and trade-off |
| --- | --- | --- |
| One global `WEBHOOK_SECRET` | Simple setup: one secret for all repos | A secret per project: one leaked secret affects one repo only; needs storing and showing per project |
| Webhook created by hand in GitHub | No GitHub app or OAuth needed | Register it automatically through the GitHub API: needs a token with admin rights on the repo |
| Match projects by repo URL and branch | One push can deploy several projects (e.g. `main` and `staging`) | Webhook URL per project (`/webhooks/github/{id}`): simpler matching, more URLs to set up |
| Cloudflare quick tunnel | Free, no account, forwards the body untouched | smee.io re-sends the JSON and can break the signature; ngrok needs an account; a real server needs none of these |
| `tunnelGuard` by header | The API has no login yet; only the signed endpoint should be public | Add authentication to the whole API (phase 5 or later) |
| Pending flag, not a list of pushes | Building the branch head covers every skipped push | Queue every push: wasted builds of commits that are already outdated |
| Rollback = new deployment row | Full history; the same code path (`release`) as a normal deploy | Flip the old row back to live: loses history, needs separate code |
| Refuse to roll back to `failed` | Its image never passed a health check | Allow it: likely to fail again |
| Dashboard polls the list every 5 s | Push-triggered deployments appear without a reload; trivial to build | A project-wide SSE stream: instant, more code |
| App logs fetched on demand (`docker logs --tail`) | Simple, no storage | Stream with `docker logs -f` over SSE, or ship logs to a log store (Loki, CloudWatch) for search and history |

---

## 6. Interview questions with answers

**How does deploy-on-push work?**
GitHub sends a webhook to my API on every push. I verify its HMAC-SHA256 signature with the shared secret, ignore anything that is not a branch push, find the projects built from that repo and branch, and queue a deployment for each. The handler answers 202 straight away; the worker does the build.

**How do you know a webhook really came from GitHub?**
The `X-Hub-Signature-256` header is an HMAC of the exact body using a secret only GitHub and I know. I compute it myself over the raw bytes and compare with `hmac.Equal`, which takes constant time so it cannot leak the answer through timing.

**What happens if someone pushes three times in a minute?**
The first push starts a deployment. The next two find one already running, so they only set a "pending" flag for the project. When the first finishes, one more deployment runs and builds the newest commit on the branch. Two builds instead of three, and the result is the latest code.

**How does rollback work, and why is it fast?**
Every build produces an image tagged with its commit, and I keep them. Rolling back creates a new deployment that reuses an old image tag, skips clone and build, and goes through the same health-checked, zero-downtime switch. It takes seconds instead of minutes.

**Why can't you roll back to a failed deployment?**
It never passed the health check, so there is no evidence it works. Only deployments that were live (now `stopped`) are offered.

**You changed an environment variable. Why does the app need a restart?**
A process reads its environment when it starts. So after saving, I restart the live image with the new variables, as a "config" deployment, with no rebuild.

**Your local API is on the internet through a tunnel. Isn't that dangerous?**
It would be, because the API has no login. A middleware rejects every request that came through Cloudflare (it carries `Cf-Connecting-Ip`) unless it is for the webhook endpoint, and that endpoint rejects anything without a valid signature. In production the API would have proper authentication and the webhook would be one public route among protected ones.

**What would you add for production?**
A secret per project, registering webhooks automatically through the GitHub API, recording which commit each push asked for, keeping only the last few images per project with rollback limited to those, and storing app logs centrally.

---

## 7. Exercises

1. **Verify a signature yourself.** In a terminal:
   ```bash
   BODY='{"ref":"refs/heads/main","repository":{"html_url":"https://github.com/dharshana-2107/hello-app"}}'
   SIG="sha256=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" | awk '{print $2}')"
   curl -X POST localhost:8080/api/webhooks/github -H "X-GitHub-Event: push" -H "X-Hub-Signature-256: $SIG" -d "$BODY"
   ```
   Then change one character of `BODY` but keep `SIG`. What status code, and why?
2. **Real push.** Set up the tunnel and the GitHub webhook (the "Deploy on git push" card shows the steps), change the text in `server.js` of your hello-app repo, and push. Watch the deployment appear with the "git push" label.
3. **Three quick pushes.** Push three small commits within a minute. How many deployments run? Which commit is live at the end?
4. **Roll back** to an older deployment and check `http://hello.localhost`. Then roll forward again.
5. **Explain out loud:** the signature check (section 2), and why rollback is a new row and not an update (section 4).
