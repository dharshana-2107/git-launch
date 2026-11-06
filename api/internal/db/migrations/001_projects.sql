-- A project = one GitHub repo the user wants deployed.
CREATE TABLE projects (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT        NOT NULL,
    repo_url    TEXT        NOT NULL,
    branch      TEXT        NOT NULL DEFAULT 'main',
    -- The app will be served at <subdomain>.localhost, so it must be unique.
    -- The UNIQUE constraint is the real guard: two requests racing for the same
    -- name cannot both win, even if both passed a "does it exist?" check.
    subdomain   TEXT        NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Environment variables injected into the app's container at deploy time.
-- A separate table (one row per variable), not a column, because a project
-- has any number of them.
CREATE TABLE env_vars (
    project_id  BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    key         TEXT   NOT NULL,
    value       TEXT   NOT NULL,
    PRIMARY KEY (project_id, key)   -- one value per key per project
);
