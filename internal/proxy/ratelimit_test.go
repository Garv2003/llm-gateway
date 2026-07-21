package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Garv2003/llm-gateway/internal/config"
	"github.com/Garv2003/llm-gateway/internal/ratelimit"
)

func TestRateLimitBurstEventually429(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	lim := ratelimit.NewLocal(1, 3) // burst 3
	h, err := New(&config.Config{UpstreamURL: upstream.URL}, nil, nil, nil, lim)
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(h)
	defer gw.Close()

	var got429 bool
	var okCount int
	for i := 0; i < 6; i++ {
		req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer client-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			okCount++
		case http.StatusTooManyRequests:
			got429 = true
			if ra := resp.Header.Get("Retry-After"); ra == "" {
				t.Error("429 response missing Retry-After header")
			}
		default:
			t.Fatalf("unexpected status %d", resp.StatusCode)
		}
	}

	if !got429 {
		t.Fatal("a burst of requests should eventually be rate limited (429)")
	}
	if okCount != 3 {
		t.Errorf("allowed = %d, want 3 (the burst allowance)", okCount)
	}
	if upstreamHits != 3 {
		t.Errorf("upstream hits = %d, want 3 (denied requests must not reach upstream)", upstreamHits)
	}
}

func TestRateLimitPerKeyIsolationOverHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	lim := ratelimit.NewLocal(1, 1) // burst 1 per key
	h, _ := New(&config.Config{UpstreamURL: upstream.URL}, nil, nil, nil, lim)
	gw := httptest.NewServer(h)
	defer gw.Close()

	do := func(key string) int {
		req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", strings.NewReader("{}"))
		req.Header.Set("X-API-Key", key)
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close()
		return resp.StatusCode
	}

	if s := do("alice"); s != http.StatusOK {
		t.Errorf("alice first = %d, want 200", s)
	}
	if s := do("alice"); s != http.StatusTooManyRequests {
		t.Errorf("alice second = %d, want 429", s)
	}
	if s := do("bob"); s != http.StatusOK {
		t.Errorf("bob first = %d, want 200 (isolated from alice)", s)
	}
}

func TestNoLimiterUnchanged(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	h, _ := New(&config.Config{UpstreamURL: upstream.URL}, nil, nil, nil, nil)
	gw := httptest.NewServer(h)
	defer gw.Close()

	for i := 0; i < 20; i++ {
		resp, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200 with no limiter", i+1, resp.StatusCode)
		}
	}
}
