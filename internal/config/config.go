package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port           int
	UpstreamURL    string
	UpstreamAPIKey string
	ModelsConfig   string

	CacheEnabled    bool
	CacheThreshold  float64
	CacheTTL        time.Duration
	CacheMaxEntries int

	EmbeddingsURL   string
	EmbeddingsKey   string
	EmbeddingsModel string
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:           8080,
		UpstreamURL:    "https://api.openai.com",
		UpstreamAPIKey: os.Getenv("UPSTREAM_API_KEY"),
		ModelsConfig:   "models.json",

		CacheEnabled:    false,
		CacheThreshold:  0.95,
		CacheTTL:        time.Hour,
		CacheMaxEntries: 1000,

		EmbeddingsURL:   os.Getenv("EMBEDDINGS_URL"),
		EmbeddingsKey:   os.Getenv("EMBEDDINGS_API_KEY"),
		EmbeddingsModel: os.Getenv("EMBEDDINGS_MODEL"),
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

	if v := os.Getenv("CACHE_ENABLED"); v != "" {
		enabled, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid CACHE_ENABLED %q: %w", v, err)
		}
		cfg.CacheEnabled = enabled
	}

	if v := os.Getenv("CACHE_THRESHOLD"); v != "" {
		threshold, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid CACHE_THRESHOLD %q: %w", v, err)
		}
		cfg.CacheThreshold = threshold
	}

	if v := os.Getenv("CACHE_TTL"); v != "" {
		ttl, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid CACHE_TTL %q: %w", v, err)
		}
		cfg.CacheTTL = ttl
	}

	if v := os.Getenv("CACHE_MAX_ENTRIES"); v != "" {
		max, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid CACHE_MAX_ENTRIES %q: %w", v, err)
		}
		cfg.CacheMaxEntries = max
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

	if c.CacheThreshold < 0 || c.CacheThreshold > 1 {
		return fmt.Errorf("CACHE_THRESHOLD must be in [0,1], got %v", c.CacheThreshold)
	}

	if c.CacheEnabled && c.EmbeddingsURL == "" {
		return fmt.Errorf("CACHE_ENABLED requires EMBEDDINGS_URL")
	}

	return nil
}
