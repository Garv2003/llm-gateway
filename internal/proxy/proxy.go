package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/Garv2003/llm-gateway/internal/config"
)

func New(cfg *config.Config) (http.Handler, error) {
	target, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream url: %w", err)
	}

	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host

			if _, ok := req.Header["User-Agent"]; !ok {
				req.Header.Set("User-Agent", "llm-gateway")
			}

			if cfg.UpstreamAPIKey != "" && req.Header.Get("Authorization") == "" {
				req.Header.Set("Authorization", "Bearer "+cfg.UpstreamAPIKey)
			}
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		},
	}

	return rp, nil
}
