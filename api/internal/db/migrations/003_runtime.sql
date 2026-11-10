-- Phase 3: deployments now RUN. New statuses:
--   deploying = image built, container starting and being health-checked
--   live      = serving traffic at <subdomain>.<domain>
--   stopped   = was live, replaced by a newer deployment
-- Phase 2's 'built' no longer exists; old rows become 'stopped'.

ALTER TABLE deployments DROP CONSTRAINT deployments_status_check;
UPDATE deployments SET status = 'stopped' WHERE status = 'built';
ALTER TABLE deployments ADD CONSTRAINT deployments_status_check
    CHECK (status IN ('queued', 'cloning', 'building', 'deploying', 'live', 'stopped', 'failed'));

-- "Unfinished" now includes deploying.
DROP INDEX deployments_one_active_per_project;
CREATE UNIQUE INDEX deployments_one_active_per_project
    ON deployments (project_id)
    WHERE status IN ('queued', 'cloning', 'building', 'deploying');

-- At most ONE live deployment per project, enforced by the database.
CREATE UNIQUE INDEX deployments_one_live_per_project
    ON deployments (project_id)
    WHERE status = 'live';

-- Which Docker container runs this deployment (empty until it starts).
ALTER TABLE deployments ADD COLUMN container_name TEXT NOT NULL DEFAULT '';
