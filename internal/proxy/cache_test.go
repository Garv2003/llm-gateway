package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Garv2003/llm-gateway/internal/cache"
	"github.com/Garv2003/llm-gateway/internal/config"
)

// staticEmbedder maps any text to the same vector, so every prompt is identical
// under cosine similarity. That makes a stored completion reusable on the next
// request, exercising the store-then-hit path deterministically and offline.
type staticEmbedder struct{}

func (staticEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}

func TestCacheServesSecondRequestWithoutUpstream(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"42"}}]}`)
	}))
	defer upstream.Close()

	sc := cache.New(staticEmbedder{}, 0.95, time.Hour, 0)
	h, err := New(&config.Config{UpstreamURL: upstream.URL}, nil, nil, sc)
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(h)
	defer gw.Close()

	req := `{"model":"gpt","messages":[{"role":"user","content":"what is the answer"}]}`

	// First request: miss, forwarded upstream, response cached.
	resp1, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(req))
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if !strings.Contains(string(body1), `"42"`) {
		t.Fatalf("first response = %q", body1)
	}

	// Second request: hit, served from cache without touching upstream.
	resp2, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(req))
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if upstreamHits != 1 {
		t.Errorf("upstream hits = %d, want 1 (second served from cache)", upstreamHits)
	}
	if resp2.Header.Get("X-Cache") != "HIT" {
		t.Errorf("X-Cache = %q, want HIT", resp2.Header.Get("X-Cache"))
	}

	var parsed struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body2, &parsed); err != nil {
		t.Fatalf("cached response not valid JSON: %v (%q)", err, body2)
	}
	if parsed.Object != "chat.completion" || len(parsed.Choices) == 0 || parsed.Choices[0].Message.Content != "42" {
		t.Errorf("cached response malformed: %q", body2)
	}
	if sc.Hits() != 1 {
		t.Errorf("cache hits = %d, want 1", sc.Hits())
	}
}

func TestStreamingBypassesCache(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: hi\n\n")
	}))
	defer upstream.Close()

	sc := cache.New(staticEmbedder{}, 0.95, time.Hour, 0)
	h, _ := New(&config.Config{UpstreamURL: upstream.URL}, nil, nil, sc)
	gw := httptest.NewServer(h)
	defer gw.Close()

	req := `{"model":"gpt","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	for i := 0; i < 2; i++ {
		resp, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(req))
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}

	if upstreamHits != 2 {
		t.Errorf("upstream hits = %d, want 2 (streaming must always bypass cache)", upstreamHits)
	}
	if sc.Hits() != 0 || sc.Misses() != 0 {
		t.Errorf("cache touched for streaming: hits=%d misses=%d, want 0/0", sc.Hits(), sc.Misses())
	}
}
