package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/Garv2003/llm-gateway/internal/cache"
	"github.com/Garv2003/llm-gateway/internal/config"
	"github.com/Garv2003/llm-gateway/internal/proxy"
	"github.com/Garv2003/llm-gateway/internal/ratelimit"
	"github.com/Garv2003/llm-gateway/internal/registry"
	"github.com/Garv2003/llm-gateway/internal/router"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	reg, err := registry.Load(cfg.ModelsConfig)
	if err != nil {
		log.Fatalf("registry: %v", err)
	}
	log.Printf("loaded model registry from %s", cfg.ModelsConfig)

	rtr := router.New(reg, nil)

	var sc *cache.SemanticCache
	if cfg.CacheEnabled {
		emb := cache.NewAPIEmbedder(cfg.EmbeddingsURL, cfg.EmbeddingsKey, cfg.EmbeddingsModel)
		sc = cache.New(emb, cfg.CacheThreshold, cfg.CacheTTL, cfg.CacheMaxEntries)
		log.Printf("semantic cache enabled (threshold=%.2f ttl=%s model=%s)", cfg.CacheThreshold, cfg.CacheTTL, cfg.EmbeddingsModel)
	}

	var lim ratelimit.Limiter
	if cfg.RateLimitEnabled {
		lim = ratelimit.NewLocal(cfg.RateLimitRPS, cfg.RateLimitBurst)
		log.Printf("rate limiting enabled (rps=%.1f burst=%d, local token bucket)", cfg.RateLimitRPS, cfg.RateLimitBurst)
	}

	p, err := proxy.New(cfg, reg, rtr, sc, lim)
	if err != nil {
		log.Fatalf("proxy: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.Handle("/v1/", p)

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("gateway listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}
