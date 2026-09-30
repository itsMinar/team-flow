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
	t.Setenv("DATABASE_URL", "postgres://localhost/db")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("APP_ENV", "staging")
	t.Setenv("LOG_LEVEL", "verbose")

	if _, err := Load(); err == nil {
		t.Fatal("Load() expected error for invalid APP_ENV and LOG_LEVEL, got nil")
	}
}

func TestLoad_ParsesOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/db")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("JWT_SECRET", "a-sufficiently-long-production-secret-value")
	t.Setenv("APP_ENV", "production")
	t.Setenv("APP_PORT", "9090")
	t.Setenv("HTTP_READ_TIMEOUT", "30s")
	// The log mail transport writes live invitation links to the log, so it is
	// rejected in production.
	t.Setenv("MAIL_TRANSPORT", "none")

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
	t.Setenv("DATABASE_URL", "postgres://localhost/db")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
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
	t.Setenv("DATABASE_URL", "postgres://localhost/db")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("JWT_SECRET", "a-sufficiently-long-production-secret-value")
	t.Setenv("APP_ENV", "production")

	if _, err := Load(); err == nil {
		t.Fatal("production must not run with the log mail transport")
	}

	t.Setenv("MAIL_TRANSPORT", "none")
	if _, err := Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}
