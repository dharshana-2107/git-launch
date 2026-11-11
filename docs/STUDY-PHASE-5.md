# Phase 5 study guide: guard rails and presentation

Phase 5 adds limits that keep one machine healthy, plus the material you show to others: CI, the README diagram, the demo script and the interview cheat sheet. About 1 hour of reading; then spend your time on `INTERVIEW-CHEATSHEET.md` and recording the demo.

Read in this order:

1. `api/internal/db/migrations/005_image_cleanup.sql`
2. `api/internal/deployment/worker.go`: `checkAppLimit`, `pruneImages`, the `--label` on `docker build`
3. `api/internal/deployment/postgres.go`: `OldImages` (the SQL), `CountLive`
4. `api/internal/deployment/runtime.go`: `RemoveImage`, `RemoveProject`
5. `api/internal/deployment/hardening_test.go`
6. `docker-compose.yml` (127.0.0.1 bindings), `.github/workflows/ci.yml`

---

## 1. Image cleanup

Every build leaves a ~50–300 MB image. Without cleanup, disk use only grows.

After each successful release, `pruneImages` asks the store for **image tags beyond the newest 5** and deletes them:

```sql
SELECT image_tag FROM deployments
WHERE project_id = $1 AND image_tag <> '' AND NOT image_pruned
GROUP BY image_tag                       -- rollbacks/config restarts share one image
HAVING bool_and(status <> 'live')        -- never the live image
ORDER BY max(created_at) DESC            -- newest USE first
OFFSET $2                                -- skip the newest 5: the rest can go
```

- **GROUP BY image_tag**: several deployments can use the same image (a rollback reuses one). Images are counted once, by their **last** use.
- **HAVING bool_and(status <> 'live')**: `bool_and` is true only if no row in the group is live, so the live image is never deleted.
- Deleted images are marked `image_pruned`, and the dashboard shows "image deleted" instead of a Roll back button. The API also refuses (409) to roll back to them.
- Images are now built with `--label launchpad.project=<id>`, so deleting a project can find and delete **all** its images with one Docker filter.

## 2. Cap on running apps

`MAX_LIVE_APPS` (default 10). Before a deployment is created, `checkAppLimit` counts live deployments:

- The project is already live → always allowed (replacing it does not add an app).
- Otherwise, at the limit → 409 "the limit of running apps is reached".

It is a **soft** limit: two first deploys at the same instant could both pass. Knowing that, and saying how you would make it hard (a lock, or a counter row updated in the same transaction), is a good interview answer.

## 3. Network exposure

- Postgres and the Traefik dashboard now listen on **127.0.0.1** only: other machines on your Wi-Fi cannot reach them.
- Apps are on the `launchpad-apps` network; Postgres is not, so an app cannot reach it by container name.
- **Honest limitation:** on Docker Desktop, a container can still reach services on your Mac through `host.docker.internal`, including Postgres on 127.0.0.1. Real isolation means running the platform's database where app containers have no route to it (another machine, or network policies). Say this if asked about isolation.

## 4. CI (GitHub Actions)

`.github/workflows/ci.yml` runs on every push to `main`:

- **API job:** `gofmt` check, `go vet`, `go test -race`
- **Web job:** `npm ci`, TypeScript check and production build

No Docker or Postgres needed, because the tests use fakes. The README shows a green/red badge. Interview line: "every push is checked for formatting, vet issues, data races and type errors."

---

## Questions to check yourself

1. Why does the cleanup query `GROUP BY image_tag` instead of just ordering deployments?
2. What stops the cleanup from deleting the image that is live right now?
3. Redeploying a live project when the app limit is reached: allowed or refused, and why?
4. Why are the tests able to run in CI without Docker or Postgres?

## Final checklist

- [ ] Push to GitHub and check the **Actions** tab is green
- [ ] Record the demo with `DEMO-SCRIPT.md`; add the link at the top of the README
- [ ] Learn the 30-second pitch and the 2-minute walkthrough in `INTERVIEW-CHEATSHEET.md` until you can say them without reading
- [ ] Put 3–4 resume bullets on your resume, with the GitHub link
