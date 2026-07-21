package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Garv2003/llm-gateway/internal/config"
	"github.com/Garv2003/llm-gateway/internal/registry"
	"github.com/Garv2003/llm-gateway/internal/router"
)

func TestForwardsAndInjectsKey(t *testing.T) {
	var gotAuth, gotPath, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	h, err := New(&config.Config{UpstreamURL: upstream.URL, UpstreamAPIKey: "secret"}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(h)
	defer gw.Close()

	resp, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if gotAuth != "Bearer secret" {
		t.Errorf("auth = %q, want injected bearer", gotAuth)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody != `{"model":"gpt"}` {
		t.Errorf("body = %q", gotBody)
	}
}

func TestPassthroughAuthNotOverwritten(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer upstream.Close()

	h, _ := New(&config.Config{UpstreamURL: upstream.URL, UpstreamAPIKey: "secret"}, nil, nil, nil, nil)
	gw := httptest.NewServer(h)
	defer gw.Close()

	req, _ := http.NewRequest("POST", gw.URL+"/v1/chat/completions", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer client-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if gotAuth != "Bearer client-key" {
		t.Errorf("auth = %q, want client key preserved", gotAuth)
	}
}

func TestStreamsIncrementally(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		io.WriteString(w, "data: chunk1\n\n")
		fl.Flush()
		<-release
		io.WriteString(w, "data: chunk2\n\n")
		fl.Flush()
	}))
	defer upstream.Close()

	h, _ := New(&config.Config{UpstreamURL: upstream.URL}, nil, nil, nil, nil)
	gw := httptest.NewServer(h)
	defer gw.Close()

	resp, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}

	reader := bufio.NewReader(resp.Body)
	done := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		done <- line
	}()

	select {
	case line := <-done:
		if !strings.Contains(line, "chunk1") {
			t.Errorf("first chunk = %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first streamed chunk before upstream finished")
	}
	close(release)
}

func TestRoutesByRequestedModel(t *testing.T) {
	var gotAuth string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer provider.Close()

	fixture := fmt.Sprintf(`{
		"providers": [{"name": "local", "baseURL": %q, "keyEnv": "LOCAL_API_KEY"}],
		"models": [{"name": "llama3.1-8b", "provider": "local", "costTier": "cheap"}]
	}`, provider.URL)
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("LOCAL_API_KEY", "local-secret")

	// Default upstream points elsewhere; a registry model must override it.
	h, err := New(&config.Config{UpstreamURL: "https://api.openai.com", UpstreamAPIKey: "default-key"}, reg, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(h)
	defer gw.Close()

	resp, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"llama3.1-8b"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q, want request routed to registry provider", string(body))
	}
	if gotAuth != "Bearer local-secret" {
		t.Errorf("auth = %q, want provider key injected", gotAuth)
	}
}

func TestUnknownModelFallsBackToDefault(t *testing.T) {
	var hit bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	fixture := `{"providers": [{"name": "openai", "baseURL": "https://api.openai.com", "keyEnv": "OPENAI_API_KEY"}], "models": []}`
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	h, _ := New(&config.Config{UpstreamURL: upstream.URL}, reg, nil, nil, nil)
	gw := httptest.NewServer(h)
	defer gw.Close()

	resp, err := http.Post(gw.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"unknown-model"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !hit {
		t.Error("unknown model did not fall back to default upstream")
	}
}

func TestAutoRouteRetriesFallbackOn5xx(t *testing.T) {
	var primaryHits, fallbackHits int

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits++
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer primary.Close()

	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits++
		io.WriteString(w, `{"ok":"fallback"}`)
	}))
	defer fallback.Close()

	fixture := fmt.Sprintf(`{
		"providers": [
			{"name": "cheapp", "baseURL": %q, "keyEnv": "CHEAP_KEY"},
			{"name": "strongp", "baseURL": %q, "keyEnv": "STRONG_KEY"}
		],
		"models": [
			{"name": "cheap-model", "provider": "cheapp", "costTier": "cheap"},
			{"name": "strong-model", "provider": "strongp", "costTier": "premium"}
		]
	}`, primary.URL, fallback.URL)
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	h, err := New(&config.Config{UpstreamURL: "https://api.openai.com"}, reg, router.New(reg, nil), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(h)
	defer gw.Close()

	// "auto" + an easy prompt routes to the cheap tier first; its 500 must
	// trigger a single retry against the premium fallback provider.
	resp, err := http.Post(gw.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 after fallback", resp.StatusCode)
	}
	if string(body) != `{"ok":"fallback"}` {
		t.Errorf("body = %q, want fallback provider response", string(body))
	}
	if primaryHits != 1 {
		t.Errorf("primary hits = %d, want 1", primaryHits)
	}
	if fallbackHits != 1 {
		t.Errorf("fallback hits = %d, want 1 (retried exactly once)", fallbackHits)
	}
}
