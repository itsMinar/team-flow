// Package config loads and validates application configuration from the
// environment. Configuration is loaded once at startup and injected into the
// rest of the application; there is no global mutable state.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// maxInvitationTTL bounds how long an invitation link stays valid. A link that
// never expires is a standing credential in a mailbox, so the lifetime is
// capped even if an operator asks for more.
const maxInvitationTTL = 30 * 24 * time.Hour

// Environment represents the runtime environment of the application.
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvTest        Environment = "test"
	EnvProduction  Environment = "production"
)

// Config holds all configuration for the application. It is populated from
// environment variables and validated at startup so the process fails fast on
// misconfiguration.
type Config struct {
	App       AppConfig
	HTTP      HTTPConfig
	Database  DatabaseConfig
	Redis     RedisConfig
	Log       LogConfig
	JWT       JWTConfig
	Invite    InvitationConfig
	Mail      MailConfig
	APIKey    APIKeyConfig
	Worker    WorkerConfig
	Jobs      JobsConfig
	RateLimit RateLimitConfig
}

// AppConfig holds general application settings.
type AppConfig struct {
	Env Environment
}

// HTTPConfig holds settings for the HTTP server.
type HTTPConfig struct {
	Port            int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	MaxBodyBytes    int64
}

// DatabaseConfig holds the PostgreSQL connection settings.
type DatabaseConfig struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// RedisConfig holds the Redis connection settings.
type RedisConfig struct {
	URL string
}

// LogConfig holds structured logging settings.
type LogConfig struct {
	Level string
}

// JWTConfig holds settings for signing and validating JSON Web Tokens and the
// lifetimes of access and refresh tokens.
type JWTConfig struct {
	Secret     string
	Issuer     string
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

// InvitationConfig holds settings for organization invitations: how long an
// invitation stays valid, and the public base URL used to build the accept link
// that is emailed to the invitee.
type InvitationConfig struct {
	TTL     time.Duration
	BaseURL string
}

// APIKeyConfig bounds how long a minted API key can stay valid. An API key is a
// long-lived credential with no rotation, so it is always given an expiry and
// the lifetime is capped.
type APIKeyConfig struct {
	DefaultTTL time.Duration
	MaxTTL     time.Duration
}

// WorkerConfig holds background worker settings: the size of the worker pool, how
// long a claim may sit unacknowledged before another worker may steal it, and
// how long shutdown waits for in-flight jobs.
type WorkerConfig struct {
	Concurrency     int
	BlockTimeout    time.Duration
	StaleAfter      time.Duration
	ShutdownTimeout time.Duration
}

// JobsConfig holds queue settings: the per-job retry policy and the key used to
// encrypt job payloads at rest in Redis.
type JobsConfig struct {
	MaxAttempts    int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
	EncryptionKey  string
}

// RateLimitPolicyConfig is a limit per period, configured from the environment.
type RateLimitPolicyConfig struct {
	Limit  int
	Period time.Duration
}

// RateLimitConfig holds the request rate limits. Each surface gets its own policy
// so machine traffic cannot consume a person's budget, and vice versa.
type RateLimitConfig struct {
	// Enabled turns limiting on. It defaults to on in production and off
	// elsewhere, so a local checkout is not throttled while a deployment is
	// protected by default.
	Enabled bool
	// FailOpen allows requests through when the limiter is unreachable.
	FailOpen bool
	// Auth applies to unauthenticated endpoints, keyed by client IP.
	Auth RateLimitPolicyConfig
	// User applies to session traffic, keyed by user.
	User RateLimitPolicyConfig
	// APIKey applies to machine traffic, keyed by API key.
	APIKey RateLimitPolicyConfig
}

// Mail transports. The log transport writes invitation links to the
// application log, which is only acceptable outside production.
const (
	MailTransportLog  = "log"
	MailTransportNone = "none"
)

// MailConfig selects how transactional email is delivered.
type MailConfig struct {
	Transport string
}

// IsProduction reports whether the application is running in production.
func (c *Config) IsProduction() bool {
	return c.App.Env == EnvProduction
}

// Load reads configuration from environment variables, applies defaults, and
// validates the result. It returns an error describing every problem found so
// the operator can fix configuration in one pass.
func Load() (*Config, error) {
	cfg := &Config{
		App: AppConfig{
			Env: Environment(getEnv("APP_ENV", string(EnvDevelopment))),
		},
		HTTP: HTTPConfig{
			Port:            getEnvInt("APP_PORT", 8080),
			ReadTimeout:     getEnvDuration("HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:    getEnvDuration("HTTP_WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:     getEnvDuration("HTTP_IDLE_TIMEOUT", 60*time.Second),
			ShutdownTimeout: getEnvDuration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
			MaxBodyBytes:    getEnvInt64("HTTP_MAX_BODY_BYTES", 1<<20), // 1 MiB
		},
		Database: DatabaseConfig{
			URL:             getEnv("DATABASE_URL", ""),
			MaxConns:        int32(getEnvInt("DATABASE_MAX_CONNS", 20)),
			MinConns:        int32(getEnvInt("DATABASE_MIN_CONNS", 2)),
			MaxConnLifetime: getEnvDuration("DATABASE_MAX_CONN_LIFETIME", time.Hour),
			MaxConnIdleTime: getEnvDuration("DATABASE_MAX_CONN_IDLE_TIME", 30*time.Minute),
		},
		Redis: RedisConfig{
			URL: getEnv("REDIS_URL", ""),
		},
		Log: LogConfig{
			Level: getEnv("LOG_LEVEL", "info"),
		},
		JWT: JWTConfig{
			Secret:     getEnv("JWT_SECRET", ""),
			Issuer:     getEnv("JWT_ISSUER", "teamflow"),
			AccessTTL:  getEnvDuration("JWT_ACCESS_TTL", 15*time.Minute),
			RefreshTTL: getEnvDuration("JWT_REFRESH_TTL", 720*time.Hour),
		},
		Mail: MailConfig{
			Transport: strings.ToLower(getEnv("MAIL_TRANSPORT", MailTransportLog)),
		},
		APIKey: APIKeyConfig{
			DefaultTTL: getEnvDuration("API_KEY_DEFAULT_TTL", 90*24*time.Hour),
			MaxTTL:     getEnvDuration("API_KEY_MAX_TTL", 365*24*time.Hour),
		},
		Worker: WorkerConfig{
			Concurrency:     getEnvInt("WORKER_CONCURRENCY", 4),
			BlockTimeout:    getEnvDuration("WORKER_BLOCK_TIMEOUT", 2*time.Second),
			StaleAfter:      getEnvDuration("WORKER_STALE_AFTER", 5*time.Minute),
			ShutdownTimeout: getEnvDuration("WORKER_SHUTDOWN_TIMEOUT", 15*time.Second),
		},
		Jobs: JobsConfig{
			MaxAttempts:    getEnvInt("WORKER_MAX_ATTEMPTS", 5),
			RetryBaseDelay: getEnvDuration("WORKER_RETRY_BASE_DELAY", 30*time.Second),
			RetryMaxDelay:  getEnvDuration("WORKER_RETRY_MAX_DELAY", time.Hour),
			EncryptionKey:  getEnv("JOB_ENCRYPTION_KEY", ""),
		},
		RateLimit: RateLimitConfig{
			FailOpen: getEnvBool("RATE_LIMIT_FAIL_OPEN", true),
			Auth: RateLimitPolicyConfig{
				Limit:  getEnvInt("RATE_LIMIT_AUTH_LIMIT", 10),
				Period: getEnvDuration("RATE_LIMIT_AUTH_PERIOD", time.Minute),
			},
			User: RateLimitPolicyConfig{
				Limit:  getEnvInt("RATE_LIMIT_USER_LIMIT", 300),
				Period: getEnvDuration("RATE_LIMIT_USER_PERIOD", time.Minute),
			},
			APIKey: RateLimitPolicyConfig{
				Limit:  getEnvInt("RATE_LIMIT_API_KEY_LIMIT", 600),
				Period: getEnvDuration("RATE_LIMIT_API_KEY_PERIOD", time.Minute),
			},
		},
		Invite: InvitationConfig{
			TTL:     getEnvDuration("INVITATION_TTL", 168*time.Hour),
			BaseURL: strings.TrimRight(getEnv("INVITATION_BASE_URL", "http://localhost:3000"), "/"),
		},
	}

	// Rate limiting is on by default in production and off elsewhere, so a local
	// checkout is not throttled while a deployment is protected by default.
	cfg.RateLimit.Enabled = getEnvBool("RATE_LIMIT_ENABLED", cfg.App.Env == EnvProduction)

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var problems []string

	switch c.App.Env {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		problems = append(problems, fmt.Sprintf("APP_ENV %q is invalid (want development|test|production)", c.App.Env))
	}

	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		problems = append(problems, fmt.Sprintf("APP_PORT %d is out of range", c.HTTP.Port))
	}

	if strings.TrimSpace(c.Database.URL) == "" {
		problems = append(problems, "DATABASE_URL is required")
	}

	if strings.TrimSpace(c.Redis.URL) == "" {
		problems = append(problems, "REDIS_URL is required")
	}

	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, fmt.Sprintf("LOG_LEVEL %q is invalid (want debug|info|warn|error)", c.Log.Level))
	}

	if strings.TrimSpace(c.JWT.Secret) == "" {
		problems = append(problems, "JWT_SECRET is required")
	} else if c.App.Env == EnvProduction && len(c.JWT.Secret) < 32 {
		problems = append(problems, "JWT_SECRET must be at least 32 characters in production")
	}
	if c.JWT.AccessTTL <= 0 {
		problems = append(problems, "JWT_ACCESS_TTL must be positive")
	}
	if c.JWT.RefreshTTL <= c.JWT.AccessTTL {
		problems = append(problems, "JWT_REFRESH_TTL must be greater than JWT_ACCESS_TTL")
	}

	if c.Invite.TTL <= 0 {
		problems = append(problems, "INVITATION_TTL must be positive")
	}
	if c.Invite.TTL > maxInvitationTTL {
		problems = append(problems, fmt.Sprintf("INVITATION_TTL must not exceed %s", maxInvitationTTL))
	}
	if err := validateAbsoluteURL("INVITATION_BASE_URL", c.Invite.BaseURL); err != nil {
		problems = append(problems, err.Error())
	}

	for _, policy := range []struct {
		label  string
		limit  int
		period time.Duration
	}{
		{"RATE_LIMIT_AUTH_LIMIT", c.RateLimit.Auth.Limit, c.RateLimit.Auth.Period},
		{"RATE_LIMIT_USER_LIMIT", c.RateLimit.User.Limit, c.RateLimit.User.Period},
		{"RATE_LIMIT_API_KEY_LIMIT", c.RateLimit.APIKey.Limit, c.RateLimit.APIKey.Period},
	} {
		if policy.limit < 1 {
			problems = append(problems, policy.label+" must be at least 1")
		}
		if policy.period <= 0 {
			problems = append(problems, "rate limit period for "+policy.label+" must be positive")
		}
	}

	if c.Worker.Concurrency < 1 {
		problems = append(problems, "WORKER_CONCURRENCY must be at least 1")
	}
	if c.Worker.BlockTimeout <= 0 {
		problems = append(problems, "WORKER_BLOCK_TIMEOUT must be positive")
	}
	if c.Worker.StaleAfter <= 0 {
		problems = append(problems, "WORKER_STALE_AFTER must be positive")
	}
	if c.Worker.ShutdownTimeout <= 0 {
		problems = append(problems, "WORKER_SHUTDOWN_TIMEOUT must be positive")
	}
	if c.Jobs.MaxAttempts < 1 {
		problems = append(problems, "WORKER_MAX_ATTEMPTS must be at least 1")
	}
	if c.Jobs.RetryBaseDelay <= 0 {
		problems = append(problems, "WORKER_RETRY_BASE_DELAY must be positive")
	}
	if c.Jobs.RetryMaxDelay < c.Jobs.RetryBaseDelay {
		problems = append(problems, "WORKER_RETRY_MAX_DELAY must be greater than or equal to WORKER_RETRY_BASE_DELAY")
	}

	if c.APIKey.DefaultTTL <= 0 {
		problems = append(problems, "API_KEY_DEFAULT_TTL must be positive")
	}
	if c.APIKey.MaxTTL <= 0 {
		problems = append(problems, "API_KEY_MAX_TTL must be positive")
	}
	if c.APIKey.MaxTTL < c.APIKey.DefaultTTL {
		problems = append(problems, "API_KEY_MAX_TTL must be greater than or equal to API_KEY_DEFAULT_TTL")
	}

	switch c.Mail.Transport {
	case MailTransportLog, MailTransportNone:
	default:
		problems = append(problems, fmt.Sprintf("MAIL_TRANSPORT %q is invalid (want log|none)", c.Mail.Transport))
	}
	// The log transport writes live invitation links to the log, so it is a
	// development convenience only. Running it in production would put
	// unexpired organization-join credentials in the log sink.
	if c.App.Env == EnvProduction && c.Mail.Transport == MailTransportLog {
		problems = append(problems,
			"MAIL_TRANSPORT must not be log in production; configure a real email sender")
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// validateAbsoluteURL requires an http(s) URL with a host so it can be used as a
// link target.
func validateAbsoluteURL(name, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%s must be an absolute http(s) URL", name)
	}
	return nil
}

func getEnvBool(key string, fallback bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if parsed, err := strconv.ParseBool(v); err == nil {
			return parsed
		}
	}
	return fallback
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvInt64(key string, fallback int64) int64 {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
