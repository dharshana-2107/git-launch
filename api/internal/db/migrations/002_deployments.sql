-- A deployment = one attempt to build (and, from phase 3, run) a project
-- at a specific commit.
CREATE TABLE deployments (
    id              BIGSERIAL PRIMARY KEY,
    project_id      BIGINT      NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- The CHECK constraint stops any code from saving a status we did not plan for.
    status          TEXT        NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued', 'cloning', 'building', 'built', 'failed')),
    commit_sha      TEXT        NOT NULL DEFAULT '',
    commit_message  TEXT        NOT NULL DEFAULT '',
    image_tag       TEXT        NOT NULL DEFAULT '',
    error           TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at      TIMESTAMPTZ,            -- NULL until a worker picks it up
    finished_at     TIMESTAMPTZ             -- NULL until built or failed
);

-- Newest-first lists per project use this index.
CREATE INDEX deployments_project_created ON deployments (project_id, created_at DESC);

-- At most ONE unfinished deployment per project, enforced by the database.
-- A "partial" unique index only covers rows matching its WHERE clause, so a
-- project can have any number of finished deployments but only one running.
-- Same lesson as the subdomain in phase 1: two Deploy clicks at the same
-- moment cannot both get through.
CREATE UNIQUE INDEX deployments_one_active_per_project
    ON deployments (project_id)
    WHERE status IN ('queued', 'cloning', 'building');

-- Build output, one row per line, so a page opened halfway through a build
-- can replay everything printed so far.
CREATE TABLE deployment_logs (
    deployment_id BIGINT      NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    line_no       INT         NOT NULL,
    text          TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (deployment_id, line_no)
);
