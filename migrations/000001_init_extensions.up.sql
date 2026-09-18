-- Enable extensions used across the schema.
-- pgcrypto provides gen_random_uuid() for UUID primary keys.
-- citext provides case-insensitive text used for email/slug uniqueness.
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;
