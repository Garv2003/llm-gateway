package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/Garv2003/llm-gateway/internal/config"
	"github.com/Garv2003/llm-gateway/internal/proxy"
	"github.com/Garv2003/llm-gateway/internal/registry"
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

	p, err := proxy.New(cfg, reg)
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
