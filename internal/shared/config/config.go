package config

import (
	"errors"
	"time"

	"github.com/caarlos0/env/v10"
	"github.com/joho/godotenv"
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
	Port         int           `env:"SERVER_PORT" envDefault:"8080"`
	Host         string        `env:"SERVER_HOST" envDefault:"0.0.0.0"`
	ReadTimeout  time.Duration `env:"SERVER_READ_TIMEOUT" envDefault:"30s"`
	WriteTimeout time.Duration `env:"SERVER_WRITE_TIMEOUT" envDefault:"30s"`
}

// JWT settings
type JWTConfig struct {
	SecretKey       string        `env:"JWT_SECRET_KEY"`
	AccessTokenTTL  time.Duration `env:"JWT_ACCESS_TOKEN_TTL" envDefault:"15m"`
	RefreshTokenTTL time.Duration `env:"JWT_REFRESH_TOKEN_TTL" envDefault:"168h"`
}

// Agent settings
type AgentConfig struct {
	GRPCPort          int           `env:"AGENT_GRPC_PORT" envDefault:"50051"`
	HeartbeatInterval time.Duration `env:"AGENT_HEARTBEAT_INTERVAL" envDefault:"30s"`
	PanelAddress      string        `env:"AGENT_PANEL_ADDRESS"`
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

// Config holds all application configuration
type Config struct {
	Database DatabaseConfig
	Server   ServerConfig
	JWT      JWTConfig
	Agent    AgentConfig
	S3       S3Config
	VNC      VNCConfig
}

// Load reads configuration from environment variables.
// It first loads .env file if it exists (for development).
// Then it overrides with environment variables.
func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{}

	if err := env.Parse(cfg); err != nil {
		return nil, errors.New("failed to parse config: " + err.Error())
	}

	if err := validate(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
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
	if cfg.JWT.SecretKey == "" {
		return errors.New("JWT_SECRET_KEY is required")
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

func LoadDefault() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
