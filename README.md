# 🥋 SENSEI

**AI assistant right in your terminal.** Ask questions in natural language and get answers — or the exact command — without leaving the command line.

---

## Installation

### Prerequisites

- [Go 1.23+](https://go.dev/dl/)
- Linux or macOS (on Windows, use WSL — command execution assumes a POSIX shell)

### Via `go install` (recommended)

```bash
go install github.com/triiltz/sensei@latest
```

The binary will be installed to `~/go/bin/`. Make sure this directory is in your `PATH`:

```bash
# Add to your ~/.zshrc or ~/.bashrc if not already present:
export PATH="$HOME/go/bin:$PATH"
```

### Manual build (and local updates)

```bash
git clone https://github.com/triiltz/sensei.git
cd sensei
go build -o sensei .
sudo mv sensei /usr/local/bin/   # optional, for global access
```

**Tip for developers:** If you make changes to the source code locally, simply run `go install .` inside the project folder. This will recompile and immediately update the binary in your `~/go/bin/` folder, making the changes instantly available to the `sensei` command.

---

## Configuration

SENSEI is configured entirely through **environment variables**. No `.env` files, no config files — clean and simple.

### 1. API Key (required)

```bash
export SENSEI_API_KEY="your-api-key-here"
```

> To persist across terminal sessions, add this line to your `~/.zshrc` (or `~/.bashrc`).

### 2. Provider (optional)

SENSEI supports two AI providers:

| Provider | Value | Where to get a key |
|----------|-------|-------------------|
| **OpenAI** (ChatGPT) | `openai` (default) | [platform.openai.com/api-keys](https://platform.openai.com/api-keys) |
| **Google Gemini** | `gemini` | [aistudio.google.com/apikey](https://aistudio.google.com/apikey) |

```bash
# To use OpenAI (default, no need to set):
export SENSEI_PROVIDER="openai"

# To use Gemini:
export SENSEI_PROVIDER="gemini"
```

> **Each provider uses its own API key.** When switching providers, make sure to update both `SENSEI_API_KEY` and `SENSEI_PROVIDER`.

#### Tip: quickly switching between providers

You can create aliases in your `~/.zshrc` to switch between them painlessly:

```bash
# In ~/.zshrc:
alias sensei-openai='export SENSEI_PROVIDER="openai" && export SENSEI_API_KEY="sk-your-openai-key"'
alias sensei-gemini='export SENSEI_PROVIDER="gemini" && export SENSEI_API_KEY="your-gemini-key"'
```

Then just run `sensei-openai` or `sensei-gemini` before using the tool.

### 3. Model (optional)

By default, SENSEI uses models optimized for speed and cost:

| Provider | Default model |
|----------|--------------|
| OpenAI | `gpt-6-luna` |
| Gemini | `gemini-3.5-flash-lite` |
| Anthropic | `claude-haiku-5-5` |

Gemini defaults to Flash-Lite because it has the most generous free-tier quota.
Free-tier limits are per model and per project, and they change over time: in
November 2026 Gemini Flash allowed only 5 requests per minute and 20 per day.
Check your current limits in [Google AI Studio](https://aistudio.google.com/rate-limit).
The older `gemini-2.5-*` models only work for accounts that used them before.

To use a different model:

```bash
export SENSEI_MODEL="gemini-3.8-flash"  # Gemini Flash (smarter, much lower free quota)
export SENSEI_MODEL="gpt-6-astra"       # OpenAI
```

### Environment variables summary

| Variable | Required | Description |
|----------|:--------:|-----------|
| `SENSEI_API_KEY` | ✅ | API key for the chosen provider |
| `SENSEI_PROVIDER` | ❌ | `openai` (default) or `gemini` |
| `SENSEI_MODEL` | ❌ | Specific model (overrides the default) |

---

## Usage

### Direct question

```bash
sensei "how to extract a tar.gz?"
```

SENSEI responds concisely and, when applicable, suggests the exact command with the option to run it immediately:

```
Extracts a tar.gz archive.

   Suggested command: tar -xzf archive.tar.gz
   Run it? [y/N]:
```

### Pipe mode (code/log analysis)

Pipe content into SENSEI for analysis:

```bash
cat script.sh | sensei "what does this script do?"
docker logs app 2>&1 | sensei "are there any errors here?"
git diff | sensei "summarize these changes"
```

In this mode, SENSEI automatically switches behavior: instead of being concise, it explains the content in a detailed and structured way.

### Composing with other tools

The response goes to stdout and the spinner to stderr, so SENSEI pipes and redirects cleanly:

```bash
sensei "explain the TCP handshake" > notes.txt
git diff | sensei "summarize these changes" | tee review.txt
```

### Flags

| Flag | Short | Description |
|------|:-----:|-----------|
| `--force` | `-f` | Execute the suggested command automatically, skipping confirmation |
| `--openai` | `-o` | Open ChatGPT in the default browser |
| `--gemini` | `-g` | Open Google Gemini in the default browser |
| `--version` | `-v` | Show the installed version |
| `--help` | `-h` | Show help |

```bash
# Execute directly without asking [y/N]:
sensei -f "list all open ports"

# Open ChatGPT in the browser:
sensei -o

# Open Gemini in the browser:
sensei -g
```

> **`-f` is rejected when input is piped.** Piped content is untrusted: a log or
> file could contain text that steers the model into suggesting a command you
> never intended, and `-f` would run it without asking. Run without `-f` and
> confirm the command yourself.

### A note on running suggested commands

SENSEI asks before running anything, but the command is written by a language
model — **read it before answering `y`**. Treat it the way you would treat a
command copied from a random blog post.

---

## How it works

```
┌───────────────────────────────────────────────────┐
│  sensei "question"    or    cat x | sensei "..."  │
└──────────────────────┬────────────────────────────┘
                       │
              ┌────────▼────────┐
              │  Detect mode:   │
              │  Terminal / Pipe │
              └────────┬────────┘
                       │
              ┌────────▼────────┐
              │  Inject correct │
              │  system prompt  │
              └────────┬────────┘
                       │
              ┌────────▼────────┐
              │  Send request   │
              │  to API (OpenAI │
              │  / Gemini)      │
              └────────┬────────┘
                       │
              ┌────────▼────────┐
              │  Parse response │
              │  COMMAND=...?   │
              └────────┬────────┘
                       │
                 ┌─────▼─────┐
                 │  Yes       │──▶ Prompt [y/N] ──▶ Execute in shell
                 │  No        │──▶ Print response
                 └───────────┘
```

---

## Project structure

```
sensei/
├── cmd/
│   └── root.go            # CLI (Cobra): flags, pipe detection, orchestration
├── internal/
│   ├── ai/
│   │   ├── client.go      # Dual HTTP client: OpenAI + Gemini
│   │   └── client_test.go
│   ├── config/
│   │   ├── config.go      # Reads environment variables (API key, provider, model)
│   │   └── config_test.go
│   └── executor/
│       ├── runner.go      # COMMAND= parsing, confirmation prompt, shell execution
│       └── runner_test.go
├── main.go                # Entry point
├── go.mod
└── go.sum
```

### Running the tests

```bash
go test ./...
```

---

## License

[MIT](LICENSE)
