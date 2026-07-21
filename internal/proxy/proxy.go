package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/Garv2003/llm-gateway/internal/config"
	"github.com/Garv2003/llm-gateway/internal/registry"
	"github.com/Garv2003/llm-gateway/internal/router"
)

type routeKey struct{}

type route struct {
	target *url.URL
	apiKey string
}

func New(cfg *config.Config, reg *registry.Registry, rtr *router.Router) (http.Handler, error) {
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
		body := drainBody(r)

		primary, fallback, hasFallback, ok := resolveRoutes(reg, rtr, body)
		if !ok {
			rp.ServeHTTP(w, r)
			return
		}

		r = r.WithContext(context.WithValue(r.Context(), routeKey{}, primary))
		if !hasFallback {
			rp.ServeHTTP(w, r)
			return
		}

		// First attempt buffers a 5xx response so we can retry it; a success
		// (or any status < 500) commits immediately and streams through.
		bi := &interceptor{rw: w, header: make(http.Header), canRetry: true}
		rp.ServeHTTP(bi, r)
		if bi.committed {
			return
		}
		if bi.status < 500 {
			bi.commit()
			return
		}

		log.Printf("router: upstream status %d, retrying once with fallback route", bi.status)
		r.Body = io.NopCloser(bytes.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), routeKey{}, fallback))
		rp.ServeHTTP(w, r)
	}), nil
}

// drainBody reads and restores the request body so it can be peeked and, on
// retry, replayed.
func drainBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body
}

// resolveRoutes decides which provider handles the request and, when a router
// is configured, an optional fallback route for retries. It returns ok=false
// when no registry model matches, so the caller uses the default upstream.
func resolveRoutes(reg *registry.Registry, rtr *router.Router, body []byte) (primary, fallback route, hasFallback, ok bool) {
	if len(body) == 0 {
		return route{}, route{}, false, false
	}

	var payload struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &payload)
	model := payload.Model

	var chosen registry.Model
	if (model == "" || model == "auto") && rtr != nil {
		dec, dok := rtr.Route(router.ExtractPrompt(body))
		if !dok {
			return route{}, route{}, false, false
		}
		chosen = dec.Model
		log.Printf("router: auto-selected model=%s tier=%s score=%.2f [%s]",
			chosen.Name, dec.Tier, dec.Score, strings.Join(dec.Reasons, ", "))
	} else {
		m, mok := reg.Model(model)
		if !mok {
			return route{}, route{}, false, false
		}
		chosen = m
	}

	primary, pok := routeForModel(reg, chosen)
	if !pok {
		return route{}, route{}, false, false
	}

	if rtr != nil {
		if fb, fok := rtr.Fallback(chosen); fok {
			if fr, frok := routeForModel(reg, fb); frok {
				return primary, fr, true, true
			}
		}
	}
	return primary, route{}, false, true
}

func routeForModel(reg *registry.Registry, m registry.Model) (route, bool) {
	provider, ok := reg.Provider(m.Provider)
	if !ok {
		return route{}, false
	}
	target, err := url.Parse(provider.BaseURL)
	if err != nil {
		return route{}, false
	}
	return route{target: target, apiKey: reg.ResolveKey(provider.Name)}, true
}

// interceptor wraps a ResponseWriter. When canRetry is set it holds a status
// >= 500 (and its body) instead of committing, letting the proxy discard it and
// retry. Any other status commits immediately and passes writes straight
// through, preserving streaming.
type interceptor struct {
	rw          http.ResponseWriter
	header      http.Header
	status      int
	buf         bytes.Buffer
	committed   bool
	passthrough bool
	canRetry    bool
}

func (b *interceptor) Header() http.Header { return b.header }

func (b *interceptor) WriteHeader(code int) {
	if b.committed || b.status != 0 {
		return
	}
	b.status = code
	if b.canRetry && code >= 500 {
		return // hold for a possible retry
	}
	b.commit()
}

func (b *interceptor) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.WriteHeader(http.StatusOK)
	}
	if b.passthrough {
		return b.rw.Write(p)
	}
	return b.buf.Write(p) // held: buffer for potential discard
}

func (b *interceptor) Flush() {
	if b.passthrough {
		if f, ok := b.rw.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// commit writes the held status, headers, and buffered body to the underlying
// writer and switches to passthrough for subsequent writes.
func (b *interceptor) commit() {
	if b.committed {
		return
	}
	status := b.status
	if status == 0 {
		status = http.StatusOK
	}
	dst := b.rw.Header()
	for k, v := range b.header {
		dst[k] = v
	}
	b.rw.WriteHeader(status)
	b.committed = true
	b.passthrough = true
	if b.buf.Len() > 0 {
		b.rw.Write(b.buf.Bytes())
		b.buf.Reset()
	}
}
