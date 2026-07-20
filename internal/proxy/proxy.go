package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/Garv2003/llm-gateway/internal/config"
	"github.com/Garv2003/llm-gateway/internal/registry"
)

type routeKey struct{}

type route struct {
	target *url.URL
	apiKey string
}

func New(cfg *config.Config, reg *registry.Registry) (http.Handler, error) {
	defaultTarget, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream url: %w", err)
	}

	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			target := defaultTarget
			apiKey := cfg.UpstreamAPIKey
			if rt, ok := req.Context().Value(routeKey{}).(route); ok {
				target = rt.target
				apiKey = rt.apiKey
			}

			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host

			if _, ok := req.Header["User-Agent"]; !ok {
				req.Header.Set("User-Agent", "llm-gateway")
			}

			if apiKey != "" && req.Header.Get("Authorization") == "" {
				req.Header.Set("Authorization", "Bearer "+apiKey)
			}
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		},
	}

	if reg == nil {
		return rp, nil
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rt, ok := routeForRequest(reg, r); ok {
			r = r.WithContext(context.WithValue(r.Context(), routeKey{}, rt))
		}
		rp.ServeHTTP(w, r)
	}), nil
}

// routeForRequest peeks at the request body for a "model" field and, if that
// model is in the registry, returns the route to its provider. The body is
// buffered and restored so the proxy can forward it unchanged.
func routeForRequest(reg *registry.Registry, r *http.Request) (route, bool) {
	if r.Body == nil {
		return route{}, false
	}

	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) == 0 {
		return route{}, false
	}

	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Model == "" {
		return route{}, false
	}

	model, ok := reg.Model(payload.Model)
	if !ok {
		return route{}, false
	}
	provider, ok := reg.Provider(model.Provider)
	if !ok {
		return route{}, false
	}
	target, err := url.Parse(provider.BaseURL)
	if err != nil {
		return route{}, false
	}

	return route{target: target, apiKey: reg.ResolveKey(provider.Name)}, true
}
