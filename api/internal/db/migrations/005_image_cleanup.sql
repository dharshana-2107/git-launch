-- Phase 5: old images are deleted to save disk (only the newest few per
-- project are kept). A deployment whose image was deleted can no longer be
-- rolled back to, and the dashboard hides its "Roll back" button.
ALTER TABLE deployments ADD COLUMN image_pruned BOOLEAN NOT NULL DEFAULT false;
