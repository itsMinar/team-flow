// Package config loads and validates application configuration from the
// environment. Configuration is loaded once at startup and injected into the
// rest of the application; there is no global mutable state.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

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
	App      AppConfig
	HTTP     HTTPConfig
	Database DatabaseConfig
	Redis    RedisConfig
	Log      LogConfig
	JWT      JWTConfig
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
	}

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

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
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
