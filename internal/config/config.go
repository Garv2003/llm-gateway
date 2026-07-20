package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
)

type Config struct {
	Port           int
	UpstreamURL    string
	UpstreamAPIKey string
	ModelsConfig   string
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:           8080,
		UpstreamURL:    "https://api.openai.com",
		UpstreamAPIKey: os.Getenv("UPSTREAM_API_KEY"),
		ModelsConfig:   "models.json",
	}

	if v := os.Getenv("PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid PORT %q: %w", v, err)
		}
		cfg.Port = port
	}

	if v := os.Getenv("UPSTREAM_URL"); v != "" {
		cfg.UpstreamURL = v
	}

	if v := os.Getenv("MODELS_CONFIG"); v != "" {
		cfg.ModelsConfig = v
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("PORT out of range: %d", c.Port)
	}

	u, err := url.Parse(c.UpstreamURL)
	if err != nil {
		return fmt.Errorf("invalid UPSTREAM_URL %q: %w", c.UpstreamURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("UPSTREAM_URL must be http or https, got %q", c.UpstreamURL)
	}
	if u.Host == "" {
		return fmt.Errorf("UPSTREAM_URL must include a host, got %q", c.UpstreamURL)
	}

	return nil
}
