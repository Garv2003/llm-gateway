// Command cost estimates the LLM spend the gateway's router saves on a sample
// workload, entirely OFFLINE. It replays each prompt through the same heuristic
// classifier the live gateway uses, then prices two scenarios against the
// registry's per-token costs:
//
//	baseline — every prompt served by the premium model (no gateway),
//	routed   — every prompt served by the model the router picks.
//
// The difference is the estimated routing savings. Because token counts come
// from a char/4 heuristic and outputs from a fixed estimate (no live API is
// called), the figure is an ESTIMATE, not an end-to-end measurement. An
// optional cache-hit-rate models the extra savings a warm semantic cache adds.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Garv2003/llm-gateway/internal/registry"
	"github.com/Garv2003/llm-gateway/internal/router"
)

type workloadItem struct {
	Name     string `json:"name,omitempty"`
	Prompt   string `json:"prompt"`
	Messages int    `json:"messages,omitempty"`
	JSON     bool   `json:"json,omitempty"`
}

// tokensFor approximates a token count from character length (~4 chars/token),
// the same rough rule the OpenAI tokenizers land near for English text.
func tokensFor(text string) int {
	t := len(text) / 4
	if t < 1 && len(text) > 0 {
		t = 1
	}
	return t
}

// promptCost prices one prompt on one model given input/output token counts.
func promptCost(m registry.Model, inputTokens, outputTokens int) float64 {
	return float64(inputTokens)/1000.0*m.InputCostPer1K +
		float64(outputTokens)/1000.0*m.OutputCostPer1K
}

// baselineModel returns the model that represents "no gateway": the most
// expensive model in the premium tier, falling back to the most expensive
// model overall when no premium tier exists.
func baselineModel(reg *registry.Registry, override string) (registry.Model, error) {
	models := reg.Models()
	if len(models) == 0 {
		return registry.Model{}, fmt.Errorf("registry has no models")
	}
	if override != "" {
		m, ok := reg.Model(override)
		if !ok {
			return registry.Model{}, fmt.Errorf("baseline model %q not found in registry", override)
		}
		return m, nil
	}

	cost := func(m registry.Model) float64 { return m.InputCostPer1K + m.OutputCostPer1K }
	sort.Slice(models, func(i, j int) bool {
		pi := models[i].CostTier == "premium"
		pj := models[j].CostTier == "premium"
		if pi != pj {
			return pi // premium tier first
		}
		if cost(models[i]) != cost(models[j]) {
			return cost(models[i]) > cost(models[j]) // then most expensive
		}
		return models[i].Name < models[j].Name
	})
	return models[0], nil
}

func loadWorkload(path string) ([]workloadItem, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "[") {
		var items []workloadItem
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, fmt.Errorf("parse JSON array workload: %w", err)
		}
		return items, nil
	}
	// JSONL: one object per non-empty line.
	var items []workloadItem
	for i, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var it workloadItem
		if err := json.Unmarshal([]byte(line), &it); err != nil {
			return nil, fmt.Errorf("parse JSONL line %d: %w", i+1, err)
		}
		items = append(items, it)
	}
	return items, nil
}

type modelStat struct {
	model registry.Model
	count int
	cost  float64
}

func main() {
	var (
		modelsPath   = flag.String("models", "models.json", "path to the model registry config")
		workloadPath = flag.String("workload", "bench/cost/workload.json", "path to the workload (JSON array or JSONL)")
		outputTokens = flag.Int("output-tokens", 500, "assumed completion length in tokens per prompt")
		baseline     = flag.String("baseline", "", "override baseline model name (default: most expensive premium model)")
		cacheHitRate = flag.Float64("cache-hit-rate", 0.0, "assumed semantic-cache hit rate [0,1] for the extra-savings estimate")
	)
	flag.Parse()

	if *cacheHitRate < 0 || *cacheHitRate > 1 {
		fmt.Fprintf(os.Stderr, "cache-hit-rate must be in [0,1], got %v\n", *cacheHitRate)
		os.Exit(2)
	}

	reg, err := registry.Load(*modelsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load registry: %v\n", err)
		os.Exit(1)
	}
	rtr := router.New(reg, nil) // nil => same HeuristicClassifier as the live gateway

	base, err := baselineModel(reg, *baseline)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline: %v\n", err)
		os.Exit(1)
	}

	items, err := loadWorkload(*workloadPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load workload: %v\n", err)
		os.Exit(1)
	}
	if len(items) == 0 {
		fmt.Fprintln(os.Stderr, "workload is empty")
		os.Exit(1)
	}

	var baselineTotal, routedTotal float64
	byModel := map[string]*modelStat{}
	byTier := map[string]int{}
	var skipped int

	for _, it := range items {
		p := router.Prompt{Text: it.Prompt, Messages: it.Messages, JSONMode: it.JSON}
		if p.Messages == 0 {
			p.Messages = 1
		}
		inTok := tokensFor(it.Prompt)

		dec, ok := rtr.Route(p)
		if !ok {
			skipped++
			continue
		}

		baselineTotal += promptCost(base, inTok, *outputTokens)
		rc := promptCost(dec.Model, inTok, *outputTokens)
		routedTotal += rc

		st := byModel[dec.Model.Name]
		if st == nil {
			st = &modelStat{model: dec.Model}
			byModel[dec.Model.Name] = st
		}
		st.count++
		st.cost += rc
		byTier[dec.Tier]++
	}

	priced := len(items) - skipped
	if priced == 0 {
		fmt.Fprintln(os.Stderr, "no prompts could be routed")
		os.Exit(1)
	}

	routingSaved := 0.0
	if baselineTotal > 0 {
		routingSaved = (baselineTotal - routedTotal) / baselineTotal * 100
	}

	// Cache further avoids upstream calls on hits; model that as a fraction of
	// the routed cost removed.
	routedWithCache := routedTotal * (1 - *cacheHitRate)
	combinedSaved := 0.0
	if baselineTotal > 0 {
		combinedSaved = (baselineTotal - routedWithCache) / baselineTotal * 100
	}

	printReport(reportData{
		priced:          priced,
		skipped:         skipped,
		outputTokens:    *outputTokens,
		baseModel:       base,
		baselineTotal:   baselineTotal,
		routedTotal:     routedTotal,
		routingSaved:    routingSaved,
		cacheHitRate:    *cacheHitRate,
		routedWithCache: routedWithCache,
		combinedSaved:   combinedSaved,
		byModel:         byModel,
		byTier:          byTier,
	})
}

type reportData struct {
	priced          int
	skipped         int
	outputTokens    int
	baseModel       registry.Model
	baselineTotal   float64
	routedTotal     float64
	routingSaved    float64
	cacheHitRate    float64
	routedWithCache float64
	combinedSaved   float64
	byModel         map[string]*modelStat
	byTier          map[string]int
}

func printReport(d reportData) {
	fmt.Println("llm-gateway cost benchmark (OFFLINE ESTIMATE)")
	fmt.Println("=============================================")
	fmt.Printf("prompts priced:     %d", d.priced)
	if d.skipped > 0 {
		fmt.Printf(" (%d skipped: no model)", d.skipped)
	}
	fmt.Println()
	fmt.Printf("output tokens/req:  %d (assumed, flag -output-tokens)\n", d.outputTokens)
	fmt.Printf("baseline model:     %s (tier=%s, $%.5f/$%.5f per 1K in/out)\n",
		d.baseModel.Name, d.baseModel.CostTier, d.baseModel.InputCostPer1K, d.baseModel.OutputCostPer1K)
	fmt.Println()

	// Routing distribution table.
	fmt.Println("routing distribution")
	fmt.Println("--------------------")
	fmt.Printf("%-16s %-10s %8s %8s   %12s\n", "MODEL", "TIER", "PROMPTS", "SHARE", "ROUTED COST")
	names := make([]string, 0, len(d.byModel))
	for n := range d.byModel {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		return d.byModel[names[i]].count > d.byModel[names[j]].count
	})
	for _, n := range names {
		st := d.byModel[n]
		share := float64(st.count) / float64(d.priced) * 100
		fmt.Printf("%-16s %-10s %8d %7.1f%%   $%11.5f\n", st.model.Name, st.model.CostTier, st.count, share, st.cost)
	}
	fmt.Println()

	// Cost summary.
	fmt.Println("cost summary")
	fmt.Println("------------")
	fmt.Printf("%-28s $%.5f\n", "baseline (all premium):", d.baselineTotal)
	fmt.Printf("%-28s $%.5f\n", "routed:", d.routedTotal)
	fmt.Printf("%-28s $%.5f\n", "saved by routing:", d.baselineTotal-d.routedTotal)
	fmt.Println()

	if d.cacheHitRate > 0 {
		fmt.Printf("%-28s %.0f%%\n", "assumed cache hit rate:", d.cacheHitRate*100)
		fmt.Printf("%-28s $%.5f\n", "routed + cache:", d.routedWithCache)
		fmt.Println()
	}

	fmt.Printf("ESTIMATED COST REDUCTION: %.1f%% (routing only)\n", d.routingSaved)
	if d.cacheHitRate > 0 {
		fmt.Printf("ESTIMATED COST REDUCTION: %.1f%% (routing + %.0f%% cache)\n", d.combinedSaved, d.cacheHitRate*100)
	}
	fmt.Println()
	fmt.Println("NOTE: estimate only — costs = registry pricing x char/4 token heuristic")
	fmt.Println("      on the sample workload; no live LLM API was called.")
}
