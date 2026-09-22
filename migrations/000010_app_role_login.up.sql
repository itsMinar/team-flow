-- Phase 3: let the application connect as a non-superuser so RLS is enforced.
-- PostgreSQL superusers and table owners bypass row security regardless of
-- FORCE, so the API and worker must connect as teamflow_app (created NOLOGIN in
-- migration 8) for the org-isolation policies to actually constrain rows.
-- Migrations continue to run as the owning superuser; only the running app and
-- worker use this role.
--
-- The dev password below matches the repository's other local-only credentials
-- and MUST be overridden in production (rotate it or provision the role out of
-- band). Never reuse it outside local development.
ALTER ROLE teamflow_app WITH LOGIN PASSWORD 'teamflow_app';

-- Re-grant on existing tables (idempotent) and ensure future tables created by
-- the owner are automatically usable by the app role, so later phases need no
-- extra grants.
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO teamflow_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO teamflow_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO teamflow_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO teamflow_app;
