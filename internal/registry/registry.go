package registry

import (
	"encoding/json"
	"fmt"
	"os"
)

type Provider struct {
	Name    string `json:"name"`
	BaseURL string `json:"baseURL"`
	KeyEnv  string `json:"keyEnv"`
}

type Model struct {
	Name            string   `json:"name"`
	Provider        string   `json:"provider"`
	CostTier        string   `json:"costTier"`
	InputCostPer1K  float64  `json:"inputCostPer1K"`
	OutputCostPer1K float64  `json:"outputCostPer1K"`
	Capabilities    []string `json:"capabilities"`
}

type Registry struct {
	providers map[string]Provider
	models    map[string]Model
}

type configFile struct {
	Providers []Provider `json:"providers"`
	Models    []Model    `json:"models"`
}

func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read models config %q: %w", path, err)
	}

	var f configFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse models config %q: %w", path, err)
	}

	r := &Registry{
		providers: make(map[string]Provider, len(f.Providers)),
		models:    make(map[string]Model, len(f.Models)),
	}

	for _, p := range f.Providers {
		if p.Name == "" {
			return nil, fmt.Errorf("provider with empty name")
		}
		r.providers[p.Name] = p
	}

	for _, m := range f.Models {
		if m.Name == "" {
			return nil, fmt.Errorf("model with empty name")
		}
		if _, ok := r.providers[m.Provider]; !ok {
			return nil, fmt.Errorf("model %q references unknown provider %q", m.Name, m.Provider)
		}
		r.models[m.Name] = m
	}

	return r, nil
}

func (r *Registry) Model(name string) (Model, bool) {
	m, ok := r.models[name]
	return m, ok
}

func (r *Registry) Provider(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

func (r *Registry) ResolveKey(providerName string) string {
	p, ok := r.providers[providerName]
	if !ok {
		return ""
	}
	return os.Getenv(p.KeyEnv)
}
