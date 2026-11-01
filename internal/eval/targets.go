package eval

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/triiltz/sensei/internal/config"
)

// ParseTargets builds the list of models to evaluate from the CLI flags.
//
//	providers: "gemini,openai"
//	models:    "gemini=gemini-2.5-flash-lite,gemini=gemini-2.5-flash" (a provider may appear
//	           several times; providers without an entry use their default model)
//	repeat:    default repeats per task
//	repeatFor: "gemini=1,openai=3,anthropic:claude-haiku-5-5=3" (provider or
//	           provider:model; the more specific key wins)
func ParseTargets(providers, models string, repeat int, repeatFor string) ([]Target, error) {
	if repeat < 1 {
		return nil, fmt.Errorf("--repeat must be at least 1")
	}

	var order []config.Provider
	add := func(p config.Provider) error {
		if config.DefaultModel(p) == "" {
			return fmt.Errorf("unknown provider %q (use gemini, openai or anthropic)", p)
		}
		if !slices.Contains(order, p) {
			order = append(order, p)
		}
		return nil
	}
	for _, p := range splitList(providers) {
		if err := add(config.Provider(p)); err != nil {
			return nil, err
		}
	}

	chosen := map[config.Provider][]string{}
	for _, kv := range splitList(models) {
		p, m, ok := strings.Cut(kv, "=")
		if !ok || m == "" {
			return nil, fmt.Errorf("--models entries look like provider=model, got %q", kv)
		}
		if err := add(config.Provider(p)); err != nil {
			return nil, err
		}
		if !slices.Contains(chosen[config.Provider(p)], m) {
			chosen[config.Provider(p)] = append(chosen[config.Provider(p)], m)
		}
	}
	if len(order) == 0 {
		return nil, fmt.Errorf("no providers selected")
	}

	overrides := map[string]int{}
	for _, kv := range splitList(repeatFor) {
		key, n, ok := strings.Cut(kv, "=")
		count, err := strconv.Atoi(n)
		if !ok || err != nil || count < 1 {
			return nil, fmt.Errorf("--repeat-for entries look like provider=N or provider:model=N, got %q", kv)
		}
		overrides[key] = count
	}

	var targets []Target
	for _, p := range order {
		ms := chosen[p]
		if len(ms) == 0 {
			ms = []string{config.DefaultModel(p)}
		}
		for _, m := range ms {
			t := Target{Provider: p, Model: m, Repeat: repeat}
			if n, ok := overrides[string(p)]; ok {
				t.Repeat = n
			}
			if n, ok := overrides[string(p)+":"+m]; ok {
				t.Repeat = n
			}
			targets = append(targets, t)
		}
	}
	return targets, nil
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
