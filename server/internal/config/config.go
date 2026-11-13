// Package config loads runtime settings from the environment, with defaults
// that let the server start against a local MySQL instance with no setup.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every tunable the server needs.
type Config struct {
	// Addr is the host:port the HTTP server listens on.
	Addr string
	// PublicDir, when set, is served as static files at the site root. It holds
	// the built Vue frontend.
	PublicDir string

	DB DBConfig

	// JWTSecret signs session tokens. The default is only safe for local
	// development and is refused when Env is "production".
	JWTSecret string
	// TokenTTL is how long an issued session token stays valid.
	TokenTTL time.Duration

	// RateLimitDisabled turns the HTTP token buckets off. Only tests and load
	// tools should set XIANGQI_RATE_LIMIT=off.
	RateLimitDisabled bool

	// AllowedOrigins lists the browser origins allowed to open a WebSocket.
	// An empty list allows any origin, which suits a same-origin deployment.
	AllowedOrigins []string

	// TurnTimeout is the per-move clock. Zero (the default) means the move clock
	// is off, i.e. unlimited time per move.
	TurnTimeout time.Duration
	// GameTimeout is the total clock per side. Zero (the default) means the total
	// clock is off, i.e. unlimited time for the whole game.
	GameTimeout time.Duration

	// Env is "development" or "production".
	Env string
}

// DBConfig describes the MySQL connection.
type DBConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	// Params are extra DSN parameters.
	Params string

	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// DSN renders the go-sql-driver/mysql connection string.
func (d DBConfig) DSN() string {
	params := d.Params
	if params == "" {
		// parseTime is required so DATETIME columns scan into time.Time, and
		// utf8mb4 keeps Chinese nicknames and move notation intact.
		params = "parseTime=true&charset=utf8mb4&collation=utf8mb4_unicode_ci&loc=Local"
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?%s",
		d.User, d.Password, d.Host, d.Port, d.Name, params)
}

// DSNWithoutDatabase renders a DSN that connects to the server without
// selecting a schema, which is what creating the database requires.
func (d DBConfig) DSNWithoutDatabase() string {
	params := "parseTime=true&charset=utf8mb4&loc=Local"
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/?%s", d.User, d.Password, d.Host, d.Port, params)
}

// IsProduction reports whether the server runs in production mode.
func (c Config) IsProduction() bool { return strings.EqualFold(c.Env, "production") }

// Load reads configuration from the environment, applying defaults.
func Load() (Config, error) {
	c := Config{
		Addr:      env("XIANGQI_ADDR", ":8080"),
		PublicDir: env("XIANGQI_PUBLIC_DIR", "web/dist"),
		DB: DBConfig{
			Host:            env("XIANGQI_DB_HOST", "127.0.0.1"),
			Port:            envInt("XIANGQI_DB_PORT", 3306),
			User:            env("XIANGQI_DB_USER", "root"),
			Password:        env("XIANGQI_DB_PASSWORD", "root"),
			Name:            env("XIANGQI_DB_NAME", "xiangqi"),
			Params:          env("XIANGQI_DB_PARAMS", ""),
			MaxOpenConns:    envInt("XIANGQI_DB_MAX_OPEN", 25),
			MaxIdleConns:    envInt("XIANGQI_DB_MAX_IDLE", 5),
			ConnMaxLifetime: envDuration("XIANGQI_DB_CONN_LIFETIME", 5*time.Minute),
		},
		JWTSecret: env("XIANGQI_JWT_SECRET", "dev-insecure-secret-change-me"),
		TokenTTL:  envDuration("XIANGQI_TOKEN_TTL", 7*24*time.Hour),
		// Both clocks default to "unlimited": no move clock and no total clock, so
		// a game is never lost on time unless an operator opts into a limit.
		TurnTimeout: envDuration("XIANGQI_TURN_TIMEOUT", 0),
		GameTimeout: envDuration("XIANGQI_GAME_TIMEOUT", 0),
		Env:         env("XIANGQI_ENV", "development"),
	}

	if raw := os.Getenv("XIANGQI_ALLOWED_ORIGINS"); raw != "" {
		for _, o := range strings.Split(raw, ",") {
			if o = strings.TrimSpace(o); o != "" {
				c.AllowedOrigins = append(c.AllowedOrigins, o)
			}
		}
	}

	if c.IsProduction() {
		if err := validateJWTSecret(c.JWTSecret); err != nil {
			return c, err
		}
	}
	// The end-to-end suite and load tests drive everything from one address, so
	// they turn the buckets off explicitly. Production leaves this at the
	// default (on): real callers arrive from many addresses.
	if strings.EqualFold(env("XIANGQI_RATE_LIMIT", "on"), "off") {
		c.RateLimitDisabled = true
	}
	if c.DB.Name == "" {
		return c, fmt.Errorf("config: XIANGQI_DB_NAME must not be empty")
	}
	return c, nil
}

// validateJWTSecret rejects production signing secrets that are placeholders or
// too weak to slow an offline brute-force down.
//
// A plain equality check against the shipped placeholder is not enough: copying
// .env.example and only changing the passwords would leave a known secret in
// place, and short/low-entropy values fall to a dictionary attack instantly.
func validateJWTSecret(secret string) error {
	if len(secret) < 32 {
		return fmt.Errorf("config: XIANGQI_JWT_SECRET must be at least 32 bytes, got %d", len(secret))
	}
	lower := strings.ToLower(secret)
	for _, placeholder := range []string{"dev-insecure", "change-me", "changeme", "secret", "password", "example"} {
		if strings.Contains(lower, placeholder) {
			return fmt.Errorf("config: XIANGQI_JWT_SECRET must not contain the placeholder %q", placeholder)
		}
	}
	distinct := make(map[rune]struct{}, len(secret))
	for _, r := range secret {
		distinct[r] = struct{}{}
	}
	if len(distinct) < 10 {
		return fmt.Errorf("config: XIANGQI_JWT_SECRET has insufficient entropy (%d distinct characters)", len(distinct))
	}
	return nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return fallback
}
