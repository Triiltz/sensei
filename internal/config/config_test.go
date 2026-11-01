package config

import (
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name         string
		apiKey       string
		provider     string
		model        string
		wantErr      string
		wantProvider Provider
		wantModel    string
	}{
		{
			name:    "missing api key is an error",
			wantErr: "API key not found",
		},
		{
			name:         "defaults to openai",
			apiKey:       "sk-test",
			wantProvider: ProviderOpenAI,
			wantModel:    "gpt-4o-mini",
		},
		{
			name:         "gemini provider gets its own default model",
			apiKey:       "test",
			provider:     "gemini",
			wantProvider: ProviderGemini,
			wantModel:    "gemini-2.5-flash",
		},
		{
			name:         "provider is case-insensitive",
			apiKey:       "test",
			provider:     "GEMINI",
			wantProvider: ProviderGemini,
			wantModel:    "gemini-2.5-flash",
		},
		{
			name:     "unknown provider is rejected",
			apiKey:   "test",
			provider: "anthropic",
			wantErr:  "unsupported provider",
		},
		{
			name:         "SENSEI_MODEL overrides the default",
			apiKey:       "sk-test",
			provider:     "openai",
			model:        "gpt-4o",
			wantProvider: ProviderOpenAI,
			wantModel:    "gpt-4o",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SENSEI_API_KEY", tt.apiKey)
			t.Setenv("SENSEI_PROVIDER", tt.provider)
			t.Setenv("SENSEI_MODEL", tt.model)

			cfg, err := Load()

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.APIKey != tt.apiKey {
				t.Errorf("APIKey = %q, want %q", cfg.APIKey, tt.apiKey)
			}
			if cfg.Provider != tt.wantProvider {
				t.Errorf("Provider = %q, want %q", cfg.Provider, tt.wantProvider)
			}
			if cfg.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", cfg.Model, tt.wantModel)
			}
		})
	}
}
