ALTER TABLE refresh_tokens
    ALTER COLUMN ip_address TYPE TEXT USING ip_address::text;