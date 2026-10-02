package config

import (
	"testing"
	"time"
)

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("JWT_SECRET", "test-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.App.Env != EnvDevelopment {
		t.Errorf("Env = %q, want %q", cfg.App.Env, EnvDevelopment)
	}
	if cfg.HTTP.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.HTTP.Port)
	}
	if cfg.HTTP.MaxBodyBytes != 1<<20 {
		t.Errorf("MaxBodyBytes = %d, want %d", cfg.HTTP.MaxBodyBytes, 1<<20)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("REDIS_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() expected error for missing DATABASE_URL and REDIS_URL, got nil")
	}
}

func TestLoad_InvalidEnvAndLevel(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/db?sslmode=verify-full")
	t.Setenv("REDIS_URL", "rediss://localhost:6379")
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("APP_ENV", "staging")
	t.Setenv("LOG_LEVEL", "verbose")

	if _, err := Load(); err == nil {
		t.Fatal("Load() expected error for invalid APP_ENV and LOG_LEVEL, got nil")
	}
}

func TestLoad_ParsesOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/db?sslmode=verify-full")
	t.Setenv("REDIS_URL", "rediss://localhost:6379")
	t.Setenv("JWT_SECRET", "a-sufficiently-long-production-secret-value")
	t.Setenv("APP_ENV", "production")
	t.Setenv("APP_PORT", "9090")
	t.Setenv("HTTP_READ_TIMEOUT", "30s")
	// The log mail transport writes live invitation links to the log, so it is
	// rejected in production.
	t.Setenv("MAIL_TRANSPORT", "none")
	t.Setenv("JOB_ENCRYPTION_KEY", "an-independent-production-job-secret-value")
	t.Setenv("INVITATION_BASE_URL", "https://app.example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.IsProduction() {
		t.Error("IsProduction() = false, want true")
	}
	if cfg.HTTP.Port != 9090 {
		t.Errorf("Port = %d, want 9090", cfg.HTTP.Port)
	}
	if cfg.HTTP.ReadTimeout != 30*time.Second {
		t.Errorf("ReadTimeout = %v, want 30s", cfg.HTTP.ReadTimeout)
	}
}

func TestLoad_InvitationDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/db?sslmode=verify-full")
	t.Setenv("REDIS_URL", "rediss://localhost:6379")
	t.Setenv("JWT_SECRET", "a-sufficiently-long-local-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Invite.TTL != 168*time.Hour {
		t.Errorf("INVITATION_TTL = %v, want 168h", cfg.Invite.TTL)
	}
	if cfg.Invite.BaseURL != "http://localhost:3000" {
		t.Errorf("INVITATION_BASE_URL = %q", cfg.Invite.BaseURL)
	}
	if cfg.Mail.Transport != MailTransportLog {
		t.Errorf("MAIL_TRANSPORT = %q, want log", cfg.Mail.Transport)
	}
}

func TestLoad_InvitationOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/db")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("JWT_SECRET", "a-sufficiently-long-local-secret")
	t.Setenv("INVITATION_TTL", "48h")
	t.Setenv("INVITATION_BASE_URL", "https://app.example.com/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Invite.TTL != 48*time.Hour {
		t.Errorf("INVITATION_TTL = %v, want 48h", cfg.Invite.TTL)
	}
	// The trailing slash is trimmed so links are built without a double slash.
	if cfg.Invite.BaseURL != "https://app.example.com" {
		t.Errorf("INVITATION_BASE_URL = %q", cfg.Invite.BaseURL)
	}
}

func TestLoad_RejectsInvalidInvitationConfig(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "zero ttl", key: "INVITATION_TTL", value: "0s"},
		{name: "negative ttl", key: "INVITATION_TTL", value: "-1h"},
		{name: "ttl beyond the cap", key: "INVITATION_TTL", value: "8760h"},
		{name: "relative base url", key: "INVITATION_BASE_URL", value: "/invitations"},
		{name: "unsupported scheme", key: "INVITATION_BASE_URL", value: "ftp://app.example.com"},
		{name: "unknown mail transport", key: "MAIL_TRANSPORT", value: "smtp"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://localhost/db")
			t.Setenv("REDIS_URL", "redis://localhost:6379")
			t.Setenv("JWT_SECRET", "a-sufficiently-long-local-secret")
			t.Setenv(test.key, test.value)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() expected an error for %s=%s", test.key, test.value)
			}
		})
	}
}

func TestLoad_RejectsLogMailTransportInProduction(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/db?sslmode=verify-full")
	t.Setenv("REDIS_URL", "rediss://localhost:6379")
	t.Setenv("JWT_SECRET", "a-sufficiently-long-production-secret-value")
	t.Setenv("APP_ENV", "production")
	t.Setenv("JOB_ENCRYPTION_KEY", "an-independent-production-job-secret-value")
	t.Setenv("INVITATION_BASE_URL", "https://app.example.com")

	if _, err := Load(); err == nil {
		t.Fatal("production must not run with the log mail transport")
	}

	t.Setenv("MAIL_TRANSPORT", "none")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoad_APIKeyDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/db")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("JWT_SECRET", "a-sufficiently-long-local-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.APIKey.DefaultTTL != 90*24*time.Hour || cfg.APIKey.MaxTTL != 365*24*time.Hour {
		t.Fatalf("unexpected API key TTLs: %+v", cfg.APIKey)
	}
}

func TestLoad_RejectsInvalidAPIKeyConfig(t *testing.T) {
	cases := []struct {
		name   string
		key    string
		value  string
		second string
	}{
		{name: "zero default ttl", key: "API_KEY_DEFAULT_TTL", value: "0s"},
		{name: "negative max ttl", key: "API_KEY_MAX_TTL", value: "-1h"},
		{name: "max below default", key: "API_KEY_DEFAULT_TTL", value: "720h", second: "24h"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://localhost/db")
			t.Setenv("REDIS_URL", "redis://localhost:6379")
			t.Setenv("JWT_SECRET", "a-sufficiently-long-local-secret")
			t.Setenv(test.key, test.value)
			if test.second != "" {
				t.Setenv("API_KEY_MAX_TTL", test.second)
			}
			if _, err := Load(); err == nil {
				t.Fatalf("Load() expected an error for %s=%s", test.key, test.value)
			}
		})
	}
}

func TestLoad_RateLimitDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/db")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("JWT_SECRET", "a-sufficiently-long-local-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// Rate limiting is off in development unless it is asked for.
	if cfg.RateLimit.Enabled {
		t.Error("RATE_LIMIT_ENABLED must default to false outside production")
	}
	if !cfg.RateLimit.FailOpen {
		t.Error("RATE_LIMIT_FAIL_OPEN must default to true so a redis outage is not an outage")
	}
	if cfg.RateLimit.Auth.Limit != 10 || cfg.RateLimit.Auth.Period != time.Minute {
		t.Errorf("unexpected auth policy: %+v", cfg.RateLimit.Auth)
	}
	if cfg.RateLimit.User.Limit != 300 || cfg.RateLimit.APIKey.Limit != 600 {
		t.Errorf("unexpected user/api key policies: %+v %+v", cfg.RateLimit.User, cfg.RateLimit.APIKey)
	}
}

func TestLoad_RateLimitDefaultsOnInProduction(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/db?sslmode=verify-full")
	t.Setenv("REDIS_URL", "rediss://localhost:6379")
	t.Setenv("JWT_SECRET", "a-sufficiently-long-production-secret-value")
	t.Setenv("APP_ENV", "production")
	t.Setenv("MAIL_TRANSPORT", "none")
	t.Setenv("JOB_ENCRYPTION_KEY", "an-independent-production-job-secret-value")
	t.Setenv("INVITATION_BASE_URL", "https://app.example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.RateLimit.Enabled {
		t.Error("rate limiting must default to enabled in production")
	}
}

func TestLoad_RejectsUnsafeProductionConfiguration(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "missing job key", key: "JOB_ENCRYPTION_KEY", value: ""},
		{name: "short job key", key: "JOB_ENCRYPTION_KEY", value: "short"},
		{name: "development job-key example", key: "JOB_ENCRYPTION_KEY", value: "use-a-long-random-job-encryption-secret"},
		{name: "job key reused for jwt", key: "JOB_ENCRYPTION_KEY", value: "a-sufficiently-long-production-secret-value"},
		{name: "http invitation origin", key: "INVITATION_BASE_URL", value: "http://app.example.com"},
		{name: "development jwt example", key: "JWT_SECRET", value: "dev-only-change-me-to-a-long-random-secret"},
		{name: "database without verified tls", key: "DATABASE_URL", value: "postgres://app:pass@db.example.com/teamflow?sslmode=disable"},
		{name: "redis without tls", key: "REDIS_URL", value: "redis://cache.example.com:6379/0"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://app:pass@db.example.com/teamflow?sslmode=verify-full")
			t.Setenv("REDIS_URL", "rediss://cache.example.com:6379/0")
			t.Setenv("JWT_SECRET", "a-sufficiently-long-production-secret-value")
			t.Setenv("JOB_ENCRYPTION_KEY", "an-independent-production-job-secret-value")
			t.Setenv("INVITATION_BASE_URL", "https://app.example.com")
			t.Setenv("APP_ENV", "production")
			t.Setenv("MAIL_TRANSPORT", "none")
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted unsafe production setting %s", test.key)
			}
		})
	}
}

func TestLoad_RejectsMalformedTypedEnvironmentOverrides(t *testing.T) {
	cases := []struct{ key, value string }{
		{"APP_PORT", "not-a-port"},
		{"HTTP_READ_TIMEOUT", "fifteen seconds"},
		{"RATE_LIMIT_FAIL_OPEN", "not-a-bool"},
	}
	for _, test := range cases {
		t.Run(test.key, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://user:pass@localhost/db")
			t.Setenv("REDIS_URL", "redis://localhost:6379/0")
			t.Setenv("JWT_SECRET", "test-secret")
			t.Setenv("APP_ENV", "development")
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted malformed %s", test.key)
			}
		})
	}
}

func TestLoad_RejectsUnsafeHTTPAndPoolBounds(t *testing.T) {
	cases := []struct{ key, value string }{
		{"HTTP_READ_TIMEOUT", "0s"},
		{"HTTP_WRITE_TIMEOUT", "-1s"},
		{"HTTP_MAX_BODY_BYTES", "0"},
		{"DATABASE_MAX_CONNS", "0"},
		{"DATABASE_MIN_CONNS", "21"},
	}
	for _, test := range cases {
		t.Run(test.key, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://user:pass@localhost/db")
			t.Setenv("REDIS_URL", "redis://localhost:6379/0")
			t.Setenv("JWT_SECRET", "test-secret")
			t.Setenv("APP_ENV", "development")
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted unsafe %s=%s", test.key, test.value)
			}
		})
	}
}

func TestLoad_RateLimitOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/db")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("JWT_SECRET", "a-sufficiently-long-local-secret")
	t.Setenv("RATE_LIMIT_ENABLED", "true")
	t.Setenv("RATE_LIMIT_FAIL_OPEN", "false")
	t.Setenv("RATE_LIMIT_AUTH_LIMIT", "5")
	t.Setenv("RATE_LIMIT_AUTH_PERIOD", "1m")
	t.Setenv("RATE_LIMIT_USER_LIMIT", "42")
	t.Setenv("RATE_LIMIT_USER_PERIOD", "30s")
	t.Setenv("RATE_LIMIT_API_KEY_LIMIT", "7")
	t.Setenv("RATE_LIMIT_API_KEY_PERIOD", "10s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.RateLimit.Enabled || cfg.RateLimit.FailOpen {
		t.Fatalf("unexpected flags: %+v", cfg.RateLimit)
	}
	if cfg.RateLimit.Auth.Limit != 5 || cfg.RateLimit.User.Limit != 42 ||
		cfg.RateLimit.User.Period != 30*time.Second || cfg.RateLimit.APIKey.Limit != 7 {
		t.Fatalf("unexpected policies: %+v", cfg.RateLimit)
	}
}

func TestLoad_RejectsInvalidRateLimitConfig(t *testing.T) {
	cases := []struct{ key, value string }{
		{"RATE_LIMIT_AUTH_LIMIT", "0"},
		{"RATE_LIMIT_USER_LIMIT", "-1"},
		{"RATE_LIMIT_API_KEY_LIMIT", "0"},
		{"RATE_LIMIT_AUTH_PERIOD", "0s"},
		{"RATE_LIMIT_USER_PERIOD", "-5m"},
		{"RATE_LIMIT_API_KEY_PERIOD", "0s"},
	}
	for _, test := range cases {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://localhost/db")
			t.Setenv("REDIS_URL", "redis://localhost:6379")
			t.Setenv("JWT_SECRET", "a-sufficiently-long-local-secret")
			t.Setenv(test.key, test.value)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() expected an error for %s=%s", test.key, test.value)
			}
		})
	}
}
