package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Garv2003/llm-gateway/internal/cache"
	"github.com/Garv2003/llm-gateway/internal/config"
	"github.com/Garv2003/llm-gateway/internal/ratelimit"
	"github.com/Garv2003/llm-gateway/internal/registry"
	"github.com/Garv2003/llm-gateway/internal/router"
)

// anonymousKey is the shared bucket used when a request carries no API key.
const anonymousKey = "anonymous"

type routeKey struct{}

type route struct {
	target *url.URL
	apiKey string
}

// New builds the proxy handler. sc and lim are optional: when non-nil (and
// enabled by config) they add the semantic cache and per-API-key rate limiting
// respectively. Passing nil for either leaves that behavior unchanged.
//
// Handler order is rate limit -> cache -> routing -> forward, so rejected
// requests never reach the cache or an upstream.
func New(cfg *config.Config, reg *registry.Registry, rtr *router.Router, sc *cache.SemanticCache, lim ratelimit.Limiter) (http.Handler, error) {
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

	var forward http.Handler = rp
	if reg != nil {
		forward = routingHandler(rp, reg, rtr)
	}

	handler := forward
	if sc != nil {
		handler = cacheHandler(sc, handler)
	}
	if lim != nil {
		handler = rateLimitHandler(lim, handler)
	}
	return handler, nil
}

// rateLimitHandler enforces a per-API-key limit before any request reaches the
// cache or an upstream. The key is taken from the Authorization: Bearer <key>
// header, falling back to X-API-Key; requests with neither share a single
// "anonymous" bucket. On denial it returns 429 with a Retry-After header and an
// OpenAI-style JSON error body.
func rateLimitHandler(lim ratelimit.Limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := apiKey(r)

		allowed, retryAfter, err := lim.Allow(r.Context(), key)
		if err != nil {
			log.Printf("ratelimit: backend error, allowing request: %v", err)
			next.ServeHTTP(w, r)
			return
		}
		if !allowed {
			writeRateLimited(w, retryAfter)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// apiKey extracts the caller's key from the Authorization bearer token, or the
// X-API-Key header, returning anonymousKey when neither is present.
func apiKey(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")); token != "" && token != auth {
			return token
		}
	}
	if k := strings.TrimSpace(r.Header.Get("X-API-Key")); k != "" {
		return k
	}
	return anonymousKey
}

// writeRateLimited emits a 429 with a Retry-After header (rounded up to whole
// seconds) and an OpenAI-style error body.
func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	secs := int(math.Ceil(retryAfter.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": "Rate limit exceeded. Retry after " + strconv.Itoa(secs) + "s.",
			"type":    "rate_limit_exceeded",
			"code":    "rate_limit_exceeded",
			"param":   nil,
		},
	})
}

// routingHandler resolves the provider route for each request and, when a
// fallback exists, retries once on a 5xx upstream response.
func routingHandler(rp *httputil.ReverseProxy, reg *registry.Registry, rtr *router.Router) http.Handler {
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
	})
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

// cacheHandler wraps the forwarding handler with the semantic cache.
//
// Streaming requests (stream: true) bypass the cache entirely: serving or
// populating the cache would require reassembling a completion from the SSE
// stream, which means buffering the whole response server-side. That defeats
// incremental token delivery and can pin large responses in memory, so
// streamed requests are always forwarded straight through.
func cacheHandler(sc *cache.SemanticCache, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := drainBody(r)

		var meta struct {
			Stream bool   `json:"stream"`
			Model  string `json:"model"`
		}
		_ = json.Unmarshal(body, &meta)

		if meta.Stream || len(body) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		prompt := router.ExtractPrompt(body).Text
		if strings.TrimSpace(prompt) == "" {
			next.ServeHTTP(w, r)
			return
		}

		if completion, hit, err := sc.Lookup(r.Context(), prompt); err != nil {
			log.Printf("cache: lookup error: %v", err)
		} else if hit {
			writeCachedCompletion(w, meta.Model, completion)
			return
		}

		// Miss: forward and capture the response so a complete, successful,
		// non-streamed completion can be stored for next time.
		cw := &captureWriter{ResponseWriter: w}
		next.ServeHTTP(cw, r)

		status := cw.status
		if status == 0 {
			status = http.StatusOK
		}
		if status < 200 || status >= 300 {
			return
		}
		if completion, ok := extractCompletion(cw.body.Bytes()); ok && completion != "" {
			if err := sc.Store(r.Context(), prompt, completion); err != nil {
				log.Printf("cache: store error: %v", err)
			}
		}
	})
}

// captureWriter passes writes through to the client while recording the status
// and body so the proxy can inspect a completed response.
type captureWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *captureWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.body.Write(p)
	return c.ResponseWriter.Write(p)
}

// writeCachedCompletion emits a well-formed OpenAI chat-completion response
// carrying the cached content.
func writeCachedCompletion(w http.ResponseWriter, model, completion string) {
	if model == "" {
		model = "cached"
	}
	resp := map[string]any{
		"id":      fmt.Sprintf("chatcmpl-cache-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": completion},
			"finish_reason": "stop",
		}},
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "HIT")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// extractCompletion pulls the assistant content from an OpenAI chat-completion
// response body.
func extractCompletion(body []byte) (string, bool) {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", false
	}
	if len(resp.Choices) == 0 {
		return "", false
	}
	return resp.Choices[0].Message.Content, true
}
