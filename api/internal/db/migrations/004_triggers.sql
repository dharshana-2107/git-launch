-- Phase 4: why a deployment happened, and redeploys of an existing image.
--   manual   = Deploy button
--   push     = GitHub webhook (git push)
--   rollback = redeploy an earlier deployment's image (no rebuild)
--   config   = restart the live image with changed environment variables
ALTER TABLE deployments ADD COLUMN trigger TEXT NOT NULL DEFAULT 'manual'
    CHECK (trigger IN ('manual', 'push', 'rollback', 'config'));

-- For rollback/config: the deployment whose image was reused.
-- ON DELETE SET NULL: deleting the old row must not delete this one.
ALTER TABLE deployments ADD COLUMN source_deployment_id BIGINT
    REFERENCES deployments(id) ON DELETE SET NULL;
