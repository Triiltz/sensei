package config

import (
	"fmt"
	"os"
	"strings"
)

// Provider represents the AI provider being used.
type Provider string

const (
	ProviderOpenAI    Provider = "openai"
	ProviderGemini    Provider = "gemini"
	ProviderAnthropic Provider = "anthropic"
)

// Default models: the cheap/fast tier of each provider. The Gemini default
// must stay on a model that is available on the free tier.
var defaultModels = map[Provider]string{
	ProviderOpenAI:    "gpt-6-luna",
	ProviderGemini:    "gemini-2.5-flash",
	ProviderAnthropic: "claude-haiku-5-5",
}

// providerKeyVars lists the per-provider key variables, in the order they are
// tried when neither SENSEI_PROVIDER nor SENSEI_API_KEY decides the provider.
var providerKeyVars = []struct {
	provider Provider
	envVar   string
}{
	{ProviderGemini, "GEMINI_API_KEY"},
	{ProviderOpenAI, "OPENAI_API_KEY"},
	{ProviderAnthropic, "ANTHROPIC_API_KEY"},
}

// Config holds the application configuration loaded from environment variables.
type Config struct {
	APIKey   string
	Provider Provider
	Model    string

	// KeyVar is the environment variable the key was read from, so error
	// messages can point at the right place.
	KeyVar string
}

// Load resolves the provider, API key and model from environment variables.
//
// The provider is chosen by the first rule that applies:
//  1. SENSEI_PROVIDER, if set.
//  2. The prefix of SENSEI_API_KEY (sk-ant- → anthropic, sk- → openai, AIza → gemini).
//  3. The first per-provider key found: GEMINI_API_KEY, OPENAI_API_KEY, ANTHROPIC_API_KEY.
//
// SENSEI_API_KEY takes precedence over the per-provider keys unless it
// clearly belongs to a different provider. SENSEI_MODEL overrides the model.
func Load() (*Config, error) {
	cfg, err := resolve()
	if err != nil {
		return nil, err
	}

	cfg.Model = os.Getenv("SENSEI_MODEL")
	if cfg.Model == "" {
		cfg.Model = defaultModels[cfg.Provider]
	}
	return cfg, nil
}

func resolve() (*Config, error) {
	senseiKey := os.Getenv("SENSEI_API_KEY")

	if explicit := os.Getenv("SENSEI_PROVIDER"); explicit != "" {
		provider := Provider(strings.ToLower(explicit))
		if _, ok := defaultModels[provider]; !ok {
			return nil, fmt.Errorf("unsupported provider %q. Use \"gemini\", \"openai\" or \"anthropic\"", explicit)
		}

		// An unrecognised prefix is trusted: key formats change, and the
		// user has told us which provider it is for.
		if senseiKey != "" {
			if inferred, ok := inferProvider(senseiKey); !ok || inferred == provider {
				return &Config{APIKey: senseiKey, Provider: provider, KeyVar: "SENSEI_API_KEY"}, nil
			}
		}

		envVar := keyVarFor(provider)
		if key := os.Getenv(envVar); key != "" {
			return &Config{APIKey: key, Provider: provider, KeyVar: envVar}, nil
		}
		if senseiKey != "" {
			return nil, fmt.Errorf("SENSEI_PROVIDER is %q, but SENSEI_API_KEY looks like a key for another provider.\n\n"+
				"Set %s, or put a %s key in SENSEI_API_KEY", provider, envVar, provider)
		}
		return nil, fmt.Errorf("no API key found for %s.\n\nSet %s or SENSEI_API_KEY", provider, envVar)
	}

	if senseiKey != "" {
		provider, ok := inferProvider(senseiKey)
		if !ok {
			return nil, fmt.Errorf("could not tell which provider SENSEI_API_KEY belongs to.\n\n" +
				"Set it explicitly:\n" +
				"  export SENSEI_PROVIDER=\"gemini\"   # or openai, anthropic")
		}
		return &Config{APIKey: senseiKey, Provider: provider, KeyVar: "SENSEI_API_KEY"}, nil
	}

	for _, kv := range providerKeyVars {
		if key := os.Getenv(kv.envVar); key != "" {
			return &Config{APIKey: key, Provider: kv.provider, KeyVar: kv.envVar}, nil
		}
	}

	return nil, fmt.Errorf(
		"API key not found.\n\n" +
			"The quickest start is free with Gemini:\n" +
			"  1. Get a key at https://aistudio.google.com/apikey\n" +
			"  2. export SENSEI_API_KEY=\"your-key\"\n\n" +
			"OpenAI and Anthropic keys work too (the provider is detected from the key).\n" +
			"To choose it explicitly: export SENSEI_PROVIDER=\"gemini\"   # or openai, anthropic",
	)
}

// inferProvider guesses the provider from a key's prefix. Anthropic keys also
// start with "sk-", so they must be checked first.
func inferProvider(key string) (Provider, bool) {
	switch {
	case strings.HasPrefix(key, "sk-ant-"):
		return ProviderAnthropic, true
	case strings.HasPrefix(key, "sk-"):
		return ProviderOpenAI, true
	case strings.HasPrefix(key, "AIza"):
		return ProviderGemini, true
	default:
		return "", false
	}
}

func keyVarFor(p Provider) string {
	for _, kv := range providerKeyVars {
		if kv.provider == p {
			return kv.envVar
		}
	}
	return ""
}
