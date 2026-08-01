package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v10"
	"github.com/joho/godotenv"
	"github.com/maburvm/panel/internal/shared/secret"
)

// Database settings
type DatabaseConfig struct {
	Host     string `env:"DB_HOST" envDefault:"localhost"`
	Port     int    `env:"DB_PORT" envDefault:"5432"`
	User     string `env:"DB_USER"`
	Password string `env:"DB_PASSWORD"`
	Name     string `env:"DB_NAME"`
	SSLMode  string `env:"DB_SSL_MODE" envDefault:"disable"`
}

// Server settings
type ServerConfig struct {
	Port           int           `env:"SERVER_PORT" envDefault:"8080"`
	Host           string        `env:"SERVER_HOST" envDefault:"0.0.0.0"`
	ReadTimeout    time.Duration `env:"SERVER_READ_TIMEOUT" envDefault:"30s"`
	WriteTimeout   time.Duration `env:"SERVER_WRITE_TIMEOUT" envDefault:"30s"`
	AllowedOrigins string        `env:"ALLOWED_ORIGINS" envDefault:"http://localhost:3000"`
}

// JWT settings
type JWTConfig struct {
	SecretKey       string        `env:"JWT_SECRET_KEY"`
	AccessTokenTTL  time.Duration `env:"JWT_ACCESS_TOKEN_TTL" envDefault:"15m"`
	RefreshTokenTTL time.Duration `env:"JWT_REFRESH_TOKEN_TTL" envDefault:"168h"`
	AESKey          string        `env:"AES_KEY"` // 32-byte key for AES-256 encryption
}

// Agent settings
type AgentConfig struct {
	GRPCPort          int           `env:"AGENT_GRPC_PORT" envDefault:"50051"`
	HeartbeatInterval time.Duration `env:"AGENT_HEARTBEAT_INTERVAL" envDefault:"30s"`
	PanelAddress      string        `env:"AGENT_PANEL_ADDRESS"`
}

// AgentServerConfig holds gRPC server configuration for the agent
type AgentServerConfig struct {
	GRPCPort        int
	BindAddress     string
	TLSCertFile     string
	TLSKeyFile      string
	Environment     string
	ShutdownTimeout time.Duration
	AuthToken       string
}

// S3 settings
type S3Config struct {
	Endpoint  string `env:"S3_ENDPOINT"`
	AccessKey string `env:"S3_ACCESS_KEY"`
	SecretKey string `env:"S3_SECRET_KEY"`
	Bucket    string `env:"S3_BUCKET"`
	Region    string `env:"S3_REGION" envDefault:"us-east-1"`
}

// VNC settings
type VNCConfig struct {
	TokenTTL     time.Duration `env:"VNC_TOKEN_TTL" envDefault:"5m"`
	ProxyTimeout time.Duration `env:"VNC_PROXY_TIMEOUT" envDefault:"10s"`
}

// Libvirt settings
type LibvirtConfig struct {
	URI                 string        `env:"LIBVIRT_URI" envDefault:"qemu:///system"`
	PoolMinSize         int           `env:"LIBVIRT_POOL_MIN_SIZE" envDefault:"2"`
	PoolMaxSize         int           `env:"LIBVIRT_POOL_MAX_SIZE" envDefault:"10"`
	HealthCheckInterval time.Duration `env:"LIBVIRT_HEALTH_CHECK_INTERVAL" envDefault:"30s"`
	ConnectTimeout      time.Duration `env:"LIBVIRT_CONNECT_TIMEOUT" envDefault:"10s"`
}

// AIConfig holds AI service configuration
type AIConfig struct {
	APIKey  string `env:"AI_API_KEY"`
	BaseURL string `env:"AI_BASE_URL"`
	Model   string `env:"AI_MODEL" envDefault:"gpt-4"`
}

// Config holds all application configuration
type Config struct {
	Database DatabaseConfig
	Server   ServerConfig
	JWT      JWTConfig
	Agent    AgentConfig
	S3       S3Config
	VNC      VNCConfig
	Libvirt  LibvirtConfig
	AI       AIConfig
}

// fileBackedSecrets maps each ordinary env var to its *_FILE companion. These are
// the sensitive inputs that deployments commonly inject from mounted files
// (Kubernetes Secrets, Docker secrets, Vault sidecars, …) rather than plain env.
var fileBackedSecrets = []struct {
	name string // ordinary env var (e.g. DB_PASSWORD)
	file string // file env var (e.g. DB_PASSWORD_FILE)
}{
	{"DB_PASSWORD", "DB_PASSWORD_FILE"},
	{"S3_ACCESS_KEY", "S3_ACCESS_KEY_FILE"},
	{"S3_SECRET_KEY", "S3_SECRET_KEY_FILE"},
	{"JWT_SECRET_KEY", "JWT_SECRET_KEY_FILE"},
	{"AES_KEY", "AES_KEY_FILE"},
}

// resolveFileSecrets reads the *_FILE companions into their ordinary env vars
// BEFORE env parsing / secret-store resolution, so file-backed secrets work with
// the existing secret semantics (env → persisted → generated).
//
// Precedence: a non-empty ordinary NAME wins; otherwise, if NAME_FILE is set, its
// contents (with terminal whitespace/newlines trimmed) are used. A missing or
// empty file is a HARD error — never silently ignored. Errors never contain
// secret contents.
func resolveFileSecrets() error {
	for _, p := range fileBackedSecrets {
		if os.Getenv(p.name) != "" {
			continue // ordinary value wins
		}
		path := os.Getenv(p.file)
		if path == "" {
			continue // neither supplied; leave to normal resolution
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%s: cannot read %s: %v", p.name, p.file, err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return fmt.Errorf("%s: %s is empty", p.name, p.file)
		}
		if err := os.Setenv(p.name, v); err != nil {
			return fmt.Errorf("%s: cannot set env from %s: %v", p.name, p.file, err)
		}
	}
	return nil
}

// Load reads configuration from environment variables.
// It first loads .env file if it exists (for development).
// Then it overrides with environment variables.
func Load() (*Config, error) {
	_ = godotenv.Load()

	if err := resolveFileSecrets(); err != nil {
		return nil, err
	}

	cfg := &Config{}

	if err := env.Parse(cfg); err != nil {
		return nil, errors.New("failed to parse config: " + err.Error())
	}

	if err := applySecrets(cfg); err != nil {
		return nil, err
	}

	if err := validate(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// applySecrets resolves the JWT and AES keys from the secret store (env override
// → persisted → auto-generated) so a single-node install needs no secret env
// vars. Called by both Load and LoadDefault so every entrypoint sees the same
// resolved, non-empty secrets. Resolution is fail-closed: an unreadable/
// malformed/invalid secrets file or a failed durable write surfaces an error
// rather than silently regenerating in memory.
func applySecrets(cfg *Config) error {
	key, err := secret.JWTSecret()
	if err != nil {
		return fmt.Errorf("failed to resolve JWT secret: %w", err)
	}
	cfg.JWT.SecretKey = key

	aes, err := secret.AESKey()
	if err != nil {
		return fmt.Errorf("failed to resolve AES key: %w", err)
	}
	cfg.JWT.AESKey = aes
	return nil
}

// DatabaseURL builds a single, URL-encoded PostgreSQL connection string from
// this config. It uses net/url + net.JoinHostPort so passwords containing
// spaces, quotes, '@', ':', '/', '?', '#', backslashes, etc. are correctly
// escaped (unlike raw key/value fmt interpolation). The database name is placed
// in the path (with a leading slash) which both lib/pq and pgx accept. No
// password is ever logged.
func (c DatabaseConfig) DatabaseURL() string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
		Path:   "/" + c.Name,
	}
	q := u.Query()
	if c.SSLMode != "" {
		q.Set("sslmode", c.SSLMode)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func validate(cfg *Config) error {
	if cfg.Database.Host == "" {
		return errors.New("DB_HOST is required")
	}
	if cfg.Database.User == "" {
		return errors.New("DB_USER is required")
	}
	if cfg.Database.Password == "" {
		return errors.New("DB_PASSWORD is required")
	}
	if cfg.Database.Name == "" {
		return errors.New("DB_NAME is required")
	}
	// JWT/AES keys are resolved by applySecrets (env → persisted → generated)
	// and normalized to exactly 32 raw bytes at the secret resolution boundary,
	// so SecretKey is always non-empty here. We keep this length check as a
	// fail-closed guard against an impossible non-32-byte normalized key.
	if len(cfg.JWT.AESKey) != 32 {
		return errors.New("AES_KEY is required and must be exactly 32 bytes")
	}
	if cfg.S3.Endpoint == "" {
		return errors.New("S3_ENDPOINT is required")
	}
	if cfg.S3.AccessKey == "" {
		return errors.New("S3_ACCESS_KEY is required")
	}
	if cfg.S3.SecretKey == "" {
		return errors.New("S3_SECRET_KEY is required")
	}
	if cfg.S3.Bucket == "" {
		return errors.New("S3_BUCKET is required")
	}

	return nil
}

// LoadDefault parses env (incl. file-backed secrets) and resolves secrets WITHOUT
// validating required fields. Backwards-compatible entrypoint used by auxiliary
// commands (seeders, scripts) that only need a best-effort config and tolerate
// missing production values. Callers that need guarantees should use
// LoadDefaultValidated.
func LoadDefault() (*Config, error) {
	_ = godotenv.Load()

	if err := resolveFileSecrets(); err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, err
	}
	if err := applySecrets(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadDefaultValidated behaves like LoadDefault but also validates required
// fields. Use this for the production server boot path so validation failures are
// caught early and clearly, without changing the semantics of LoadDefault for
// existing callers.
func LoadDefaultValidated() (*Config, error) {
	cfg, err := LoadDefault()
	if err != nil {
		return nil, err
	}
	if err := validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
