package eval

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseTargets(t *testing.T) {
	tests := []struct {
		name                         string
		providers, models, repeatFor string
		repeat                       int
		want, wantErr                string
	}{
		{
			name: "default models", providers: "gemini,openai", repeat: 1,
			want: "gemini/gemini-2.5-flash-lite×1 openai/gpt-6-luna×1",
		},
		{
			name: "several models for one provider", providers: "gemini", repeat: 1,
			models: "gemini=gemini-2.5-flash-lite,gemini=gemini-2.5-flash",
			want:   "gemini/gemini-2.5-flash-lite×1 gemini/gemini-2.5-flash×1",
		},
		{
			name: "free tier once, paid three times", providers: "gemini,openai,anthropic", repeat: 3,
			repeatFor: "gemini=1",
			want:      "gemini/gemini-2.5-flash-lite×1 openai/gpt-6-luna×3 anthropic/claude-haiku-5-5×3",
		},
		{
			name: "model-specific repeat wins", providers: "gemini", repeat: 1,
			models:    "gemini=gemini-2.5-flash-lite,gemini=gemini-2.5-flash",
			repeatFor: "gemini=2,gemini:gemini-2.5-flash=1",
			want:      "gemini/gemini-2.5-flash-lite×2 gemini/gemini-2.5-flash×1",
		},
		{
			name: "--models adds its provider", providers: "gemini", repeat: 1,
			models: "anthropic=claude-haiku-5-5",
			want:   "gemini/gemini-2.5-flash-lite×1 anthropic/claude-haiku-5-5×1",
		},
		{name: "unknown provider", providers: "mistral", repeat: 1, wantErr: "unknown provider"},
		{name: "bad models entry", providers: "gemini", models: "flash", repeat: 1, wantErr: "provider=model"},
		{name: "bad repeat-for", providers: "gemini", repeatFor: "gemini=0", repeat: 1, wantErr: "provider=N"},
		{name: "zero repeat", providers: "gemini", repeat: 0, wantErr: "--repeat"},
		{name: "nothing selected", repeat: 1, wantErr: "no providers"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targets, err := ParseTargets(tt.providers, tt.models, tt.repeat, tt.repeatFor)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got []string
			for _, tg := range targets {
				got = append(got, fmt.Sprintf("%s×%d", tg, tg.Repeat))
			}
			if strings.Join(got, " ") != tt.want {
				t.Errorf("got  %s\nwant %s", strings.Join(got, " "), tt.want)
			}
		})
	}
}
