package config

import (
	"fmt"
	"net"
	"net/url"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	databaseURL string
	redactedURL string
	Port        string
	Env         string
}

func (c *Config) DSN() string { return c.databaseURL }

func (c *Config) String() string {
	return fmt.Sprintf("Config{DatabaseURL: %s, Port: %s, Env: %s}", c.redactedURL, c.Port, c.Env)
}

type postgresConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	DB       string
	SSLMode  string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	port := getEnvOrDefault("PORT", "8080")
	env := getEnvOrDefault("ENV", "development")

	if raw := os.Getenv("DATABASE_URL"); raw != "" {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid DATABASE_URL: %w", err)
		}
		return &Config{
			databaseURL: raw,
			redactedURL: u.Redacted(),
			Port:        port,
			Env:         env,
		}, nil
	}

	pg := postgresConfig{
		Host:     getEnvOrDefault("POSTGRES_HOST", "localhost"),
		Port:     os.Getenv("POSTGRES_PORT"),
		User:     os.Getenv("POSTGRES_USER"),
		Password: os.Getenv("POSTGRES_PASSWORD"),
		DB:       os.Getenv("POSTGRES_DB"),
		SSLMode:  getEnvOrDefault("POSTGRES_SSLMODE", "require"),
	}

	if err := pg.validate(); err != nil {
		return nil, err
	}

	u := pg.url()
	return &Config{
		databaseURL: u.String(),
		redactedURL: u.Redacted(),
		Port:        port,
		Env:         env,
	}, nil
}

func (pg postgresConfig) validate() error {
	missing := []string{}
	if pg.Port == "" {
		missing = append(missing, "POSTGRES_PORT")
	}
	if pg.User == "" {
		missing = append(missing, "POSTGRES_USER")
	}
	if pg.Password == "" {
		missing = append(missing, "POSTGRES_PASSWORD")
	}
	if pg.DB == "" {
		missing = append(missing, "POSTGRES_DB")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required env vars: %v", missing)
	}
	return nil
}

func (pg postgresConfig) url() *url.URL {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(pg.User, pg.Password),
		Host:   net.JoinHostPort(pg.Host, pg.Port),
		Path:   "/" + pg.DB,
	}
	q := u.Query()
	q.Set("sslmode", pg.SSLMode)
	u.RawQuery = q.Encode()
	return u
}

func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
