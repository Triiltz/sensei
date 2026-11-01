package config

import (
	"strings"
	"testing"
	"time"
)

var allVars = []string{
	"SENSEI_API_KEY", "SENSEI_PROVIDER", "SENSEI_MODEL", "SENSEI_MAX_STEPS", "SENSEI_CMD_TIMEOUT",
	"GEMINI_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY",
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantErr      string
		wantProvider Provider
		wantKey      string
		wantKeyVar   string
		wantModel    string
	}{
		{
			name:    "no key at all points at the free Gemini setup",
			env:     map[string]string{},
			wantErr: "aistudio.google.com",
		},

		// Rule 1: SENSEI_PROVIDER.
		{
			name:         "explicit provider with a matching key",
			env:          map[string]string{"SENSEI_PROVIDER": "gemini", "SENSEI_API_KEY": "AIza-test"},
			wantProvider: ProviderGemini,
			wantKey:      "AIza-test",
			wantKeyVar:   "SENSEI_API_KEY",
			wantModel:    "gemini-2.5-flash-lite",
		},
		{
			name:         "explicit provider trusts a key with an unknown prefix",
			env:          map[string]string{"SENSEI_PROVIDER": "gemini", "SENSEI_API_KEY": "custom-key"},
			wantProvider: ProviderGemini,
			wantKey:      "custom-key",
			wantKeyVar:   "SENSEI_API_KEY",
			wantModel:    "gemini-2.5-flash-lite",
		},
		{
			name:         "provider is case-insensitive",
			env:          map[string]string{"SENSEI_PROVIDER": "ANTHROPIC", "SENSEI_API_KEY": "sk-ant-test"},
			wantProvider: ProviderAnthropic,
			wantKey:      "sk-ant-test",
			wantKeyVar:   "SENSEI_API_KEY",
			wantModel:    "claude-haiku-5-5",
		},
		{
			name: "explicit provider falls back to its own key when SENSEI_API_KEY belongs elsewhere",
			env: map[string]string{
				"SENSEI_PROVIDER":   "anthropic",
				"SENSEI_API_KEY":    "AIza-gemini",
				"ANTHROPIC_API_KEY": "sk-ant-test",
			},
			wantProvider: ProviderAnthropic,
			wantKey:      "sk-ant-test",
			wantKeyVar:   "ANTHROPIC_API_KEY",
			wantModel:    "claude-haiku-5-5",
		},
		{
			name:         "explicit provider with only its per-provider key",
			env:          map[string]string{"SENSEI_PROVIDER": "openai", "OPENAI_API_KEY": "sk-test"},
			wantProvider: ProviderOpenAI,
			wantKey:      "sk-test",
			wantKeyVar:   "OPENAI_API_KEY",
			wantModel:    "gpt-6-luna",
		},
		{
			name:    "explicit provider with a mismatched key and no fallback",
			env:     map[string]string{"SENSEI_PROVIDER": "openai", "SENSEI_API_KEY": "AIza-gemini"},
			wantErr: "OPENAI_API_KEY",
		},
		{
			name:    "explicit provider with no key",
			env:     map[string]string{"SENSEI_PROVIDER": "gemini"},
			wantErr: "no API key found for gemini",
		},
		{
			name:    "unknown provider is rejected",
			env:     map[string]string{"SENSEI_PROVIDER": "mistral", "SENSEI_API_KEY": "x"},
			wantErr: "unsupported provider",
		},

		// Rule 2: SENSEI_API_KEY prefix.
		{
			name:         "existing OpenAI users keep working with no config changes",
			env:          map[string]string{"SENSEI_API_KEY": "sk-proj-test"},
			wantProvider: ProviderOpenAI,
			wantKey:      "sk-proj-test",
			wantKeyVar:   "SENSEI_API_KEY",
			wantModel:    "gpt-6-luna",
		},
		{
			name:         "an AI Studio key alone selects gemini",
			env:          map[string]string{"SENSEI_API_KEY": "AIzaSyTest"},
			wantProvider: ProviderGemini,
			wantKey:      "AIzaSyTest",
			wantKeyVar:   "SENSEI_API_KEY",
			wantModel:    "gemini-2.5-flash-lite",
		},
		{
			name:         "sk-ant- is anthropic, not openai",
			env:          map[string]string{"SENSEI_API_KEY": "sk-ant-api03-test"},
			wantProvider: ProviderAnthropic,
			wantKey:      "sk-ant-api03-test",
			wantKeyVar:   "SENSEI_API_KEY",
			wantModel:    "claude-haiku-5-5",
		},
		{
			name:    "unknown prefix without SENSEI_PROVIDER is not guessed",
			env:     map[string]string{"SENSEI_API_KEY": "mystery-key"},
			wantErr: "SENSEI_PROVIDER",
		},
		{
			name: "SENSEI_API_KEY takes precedence over per-provider keys",
			env: map[string]string{
				"SENSEI_API_KEY": "sk-test",
				"GEMINI_API_KEY": "AIza-test",
			},
			wantProvider: ProviderOpenAI,
			wantKey:      "sk-test",
			wantKeyVar:   "SENSEI_API_KEY",
			wantModel:    "gpt-6-luna",
		},

		// Rule 3: per-provider keys, in order.
		{
			name: "gemini key wins over the others",
			env: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-test",
				"OPENAI_API_KEY":    "sk-test",
				"GEMINI_API_KEY":    "AIza-test",
			},
			wantProvider: ProviderGemini,
			wantKey:      "AIza-test",
			wantKeyVar:   "GEMINI_API_KEY",
			wantModel:    "gemini-2.5-flash-lite",
		},
		{
			name: "openai key wins over anthropic",
			env: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-test",
				"OPENAI_API_KEY":    "sk-test",
			},
			wantProvider: ProviderOpenAI,
			wantKey:      "sk-test",
			wantKeyVar:   "OPENAI_API_KEY",
			wantModel:    "gpt-6-luna",
		},
		{
			name:         "anthropic key alone",
			env:          map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test"},
			wantProvider: ProviderAnthropic,
			wantKey:      "sk-ant-test",
			wantKeyVar:   "ANTHROPIC_API_KEY",
			wantModel:    "claude-haiku-5-5",
		},

		// Model override.
		{
			name:         "SENSEI_MODEL overrides the default",
			env:          map[string]string{"SENSEI_API_KEY": "sk-test", "SENSEI_MODEL": "gpt-6-astra"},
			wantProvider: ProviderOpenAI,
			wantKey:      "sk-test",
			wantKeyVar:   "SENSEI_API_KEY",
			wantModel:    "gpt-6-astra",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, v := range allVars {
				t.Setenv(v, tt.env[v])
			}

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
			if cfg.Provider != tt.wantProvider {
				t.Errorf("Provider = %q, want %q", cfg.Provider, tt.wantProvider)
			}
			if cfg.APIKey != tt.wantKey {
				t.Errorf("APIKey = %q, want %q", cfg.APIKey, tt.wantKey)
			}
			if cfg.KeyVar != tt.wantKeyVar {
				t.Errorf("KeyVar = %q, want %q", cfg.KeyVar, tt.wantKeyVar)
			}
			if cfg.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", cfg.Model, tt.wantModel)
			}
		})
	}
}

func TestLoadAgentLimits(t *testing.T) {
	tests := []struct {
		name        string
		maxSteps    string
		cmdTimeout  string
		wantSteps   int
		wantTimeout time.Duration
		wantErr     string
	}{
		{name: "defaults", wantSteps: 8, wantTimeout: 30 * time.Second},
		{name: "overrides", maxSteps: "3", cmdTimeout: "2m", wantSteps: 3, wantTimeout: 2 * time.Minute},
		{name: "plain seconds", cmdTimeout: "45", wantSteps: 8, wantTimeout: 45 * time.Second},
		{name: "zero steps", maxSteps: "0", wantErr: "SENSEI_MAX_STEPS"},
		{name: "non-numeric steps", maxSteps: "many", wantErr: "SENSEI_MAX_STEPS"},
		{name: "bad timeout", cmdTimeout: "soon", wantErr: "SENSEI_CMD_TIMEOUT"},
		{name: "negative timeout", cmdTimeout: "-5s", wantErr: "SENSEI_CMD_TIMEOUT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, v := range allVars {
				t.Setenv(v, "")
			}
			t.Setenv("SENSEI_API_KEY", "AIza-test")
			t.Setenv("SENSEI_MAX_STEPS", tt.maxSteps)
			t.Setenv("SENSEI_CMD_TIMEOUT", tt.cmdTimeout)

			cfg, err := Load()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.MaxSteps != tt.wantSteps || cfg.CmdTimeout != tt.wantTimeout {
				t.Errorf("got %d steps / %v, want %d / %v", cfg.MaxSteps, cfg.CmdTimeout, tt.wantSteps, tt.wantTimeout)
			}
		})
	}
}

func TestLoadFor(t *testing.T) {
	for _, v := range allVars {
		t.Setenv(v, "")
	}
	t.Setenv("SENSEI_API_KEY", "AIza-gemini")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("SENSEI_MODEL", "ignored-by-LoadFor")

	cfg, err := LoadFor(ProviderGemini, "")
	if err != nil || cfg.APIKey != "AIza-gemini" || cfg.Model != "gemini-2.5-flash-lite" {
		t.Errorf("LoadFor(gemini) = %+v, %v", cfg, err)
	}

	cfg, err = LoadFor(ProviderAnthropic, "claude-test")
	if err != nil || cfg.APIKey != "sk-ant-test" || cfg.KeyVar != "ANTHROPIC_API_KEY" || cfg.Model != "claude-test" {
		t.Errorf("LoadFor(anthropic) = %+v, %v", cfg, err)
	}

	if _, err := LoadFor(ProviderOpenAI, ""); err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Errorf("LoadFor(openai) without a key: err = %v", err)
	}
}
