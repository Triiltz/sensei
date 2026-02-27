package config

import (
	"fmt"
	"os"
	"strings"
)

// Provider represents the AI provider being used.
type Provider string

const (
	ProviderOpenAI Provider = "openai"
	ProviderGemini Provider = "gemini"
)

// Config holds the application configuration loaded from environment variables.
type Config struct {
	APIKey   string
	Provider Provider
	Model    string
}

// Load reads the API key and provider configuration from environment variables.
// It checks SENSEI_API_KEY (required) and SENSEI_PROVIDER (optional, defaults to "openai").
// SENSEI_MODEL can override the default model for the chosen provider.
func Load() (*Config, error) {
	apiKey := os.Getenv("SENSEI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf(
			"API key not found.\n\n" +
				"Set your key with:\n" +
				"  export SENSEI_API_KEY=\"your-api-key-here\"\n\n" +
				"Supported providers: openai (default), gemini\n" +
				"  export SENSEI_PROVIDER=\"gemini\"   # optional",
		)
	}

	provider := Provider(strings.ToLower(os.Getenv("SENSEI_PROVIDER")))
	if provider == "" {
		provider = ProviderOpenAI
	}

	if provider != ProviderOpenAI && provider != ProviderGemini {
		return nil, fmt.Errorf("unsupported provider %q. Use \"openai\" or \"gemini\"", provider)
	}

	model := os.Getenv("SENSEI_MODEL")
	if model == "" {
		switch provider {
		case ProviderOpenAI:
			model = "gpt-4o-mini"
		case ProviderGemini:
			model = "gemini-2.5-flash"
		}
	}

	return &Config{
		APIKey:   apiKey,
		Provider: provider,
		Model:    model,
	}, nil
}
