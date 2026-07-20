package proxy

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Garv2003/llm-gateway/internal/config"
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

	h, err := New(&config.Config{UpstreamURL: upstream.URL, UpstreamAPIKey: "secret"})
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

	h, _ := New(&config.Config{UpstreamURL: upstream.URL, UpstreamAPIKey: "secret"})
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

	h, _ := New(&config.Config{UpstreamURL: upstream.URL})
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
