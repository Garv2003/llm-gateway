package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// APIEmbedder calls an OpenAI-compatible /v1/embeddings endpoint. Base URL, key,
// and model are supplied from config (env). It is intentionally minimal.
type APIEmbedder struct {
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
}

// NewAPIEmbedder builds an APIEmbedder using the default HTTP client.
func NewAPIEmbedder(baseURL, apiKey, model string) *APIEmbedder {
	return &APIEmbedder{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		Client:  http.DefaultClient,
	}
}

func (e *APIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	payload, err := json.Marshal(map[string]any{
		"model": e.Model,
		"input": text,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL+"/v1/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}

	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
		return nil, fmt.Errorf("embeddings api: status %d: %s", resp.StatusCode, string(b))
	}

	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embeddings api: decode response: %w", err)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("embeddings api: empty response")
	}
	return out.Data[0].Embedding, nil
}
