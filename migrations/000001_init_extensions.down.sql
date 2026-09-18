-- Extensions are left in place on rollback; dropping them could break other
-- objects. Downgrading extensions is intentionally a no-op.
DROP EXTENSION IF EXISTS citext;
DROP EXTENSION IF EXISTS pgcrypto;
