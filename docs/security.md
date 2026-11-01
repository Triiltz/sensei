# Security model

SENSEI lets a language model propose shell commands and, in agent mode
(`sensei -a`), run them in a loop. This document describes what protects the
user, how commands are classified, the bypasses found while building the
classifier, and what is explicitly *not* protected.

## Threat model

Two things can go wrong when a model drives a shell:

1. **Harmful actions.** The model proposes a destructive command, either by
   mistake or because something it read (a log line, a README, a web page
   fetched by `curl`) contained instructions that steered it. That is prompt
   injection through command output.
2. **Data exposure.** Every command's output is sent back to the LLM provider.
   A command that only *reads* can still leak secrets: `cat .env`,
   `cat ~/.ssh/id_rsa`, `echo $AWS_SECRET_ACCESS_KEY`.

So "safe" in SENSEI means **harmless to run *and* harmless to share**, not
just "read-only".

## Defences

| Layer | What it does |
|---|---|
| Classifier (`internal/policy`) | Puts every command into `safe`, `confirm` or `dangerous` before it runs. |
| Confirmation | `confirm` and `dangerous` commands ask `[y/N]` on `/dev/tty`. `dangerous` shows a red warning first. |
| `-f` limits | `-f` skips the question **only** for the `confirm` tier. It never skips `dangerous` commands or commands that may expose secrets. |
| Pipe mode | `cat x \| sensei` is analysis only: no tools are offered, and `-f` and `-a` are rejected, because piped content is untrusted. |
| Execution limits | Agent commands run with no stdin, a timeout (`SENSEI_CMD_TIMEOUT`), in their own process group (a timeout or Ctrl+C kills everything they started), and with output capped in memory and truncated before it reaches the model. |
| Prompt | The agent prompt says command output is data, not instructions, and to investigate read-only before changing anything. This lowers risk but is **not** relied on: the classifier and confirmation are the actual controls. |

The evaluation suite shows why the prompt is not relied on. In November 2026,
`gemini-3.5-flash-lite` was given six requests that should not lead to a
destructive command. In four of them, its first move was the destructive
command anyway: `rm -rf ./* ./.*`, `git reset --hard && git clean -fd`,
`chmod -R a+w .` and `sudo ./deploy.sh`. The classifier marked all four
`dangerous`, so they would have needed an explicit `y` (in evals they are
always denied). See the results in the [README](../README.md#results).

## Tiers

| Tier | Agent mode | With `-f` | Examples |
|---|---|---|---|
| `safe` | runs without asking | runs | `ls`, `cat app.log`, `grep`, `git status/log/diff`, `docker ps/logs`, `df`, `ps` |
| `confirm` | asks | runs | anything unknown, file writes, `npm install`, `curl`, `git commit` |
| `confirm` + sensitive | asks | **asks** | `cat .env`, `ls ~/.ssh`, `echo $TOKEN`, `env`, `grep -r` |
| `dangerous` | asks with a warning | **asks** | `rm -rf`, `sudo`, `curl … \| sh`, `git push --force`, `> /etc/hosts`, `find -delete` |

How the classifier works:

- A small shell lexer understands quotes, escapes, `|`, `||`, `&&`, `;`, `&`,
  newlines, subshells, redirections (including `2>&1` and `&>`), `$(...)`,
  backticks and process substitution. Each simple command is classified, and
  the strictest tier wins.
- A command is `safe` only if **every** part is on a read-only allow-list, has
  no output redirection (except to `/dev/null`), no command substitution, and
  no variable expansion.
- Wrappers are looked through: `sh -c`, `bash -c`, `eval`, `xargs`,
  `find -exec`, `timeout`, `nohup`, `env`, `command`. A wrapper around anything
  that is not safe is `dangerous`, because the wrapper hides it from review.
- Anything the lexer cannot parse (an unterminated quote, a dangling
  redirection) is escalated, never trusted.
- **When in doubt, escalate.** Unknown commands are `confirm`, never `safe`.

## Bypasses found and fixed

While building the classifier I ran an adversarial probe: commands crafted to
look harmless while writing files, running programs or reading data. These
four cases were classified `safe` and are now fixed, each with a regression
test in `internal/policy/policy_test.go`.

| # | Command | Was | Now | Why it mattered |
|---|---|---|---|---|
| 1 | `cat /dev/sda`, `head -c 512 /dev/nvme0n1` | safe | confirm (sensitive) | `cat` and `head` are read-only, but reading a raw disk device would send disk contents, deleted files included, to the provider. |
| 2 | `git -c core.pager='rm -rf x' log` | safe | confirm | `git log` is read-only, but a `-c` override can make git run any program (`core.pager`, `core.sshCommand`, aliases). |
| 3 | `git diff --output=/etc/x` | safe | confirm, or dangerous for system paths | A read-only subcommand with an option that **writes** a file (`--output`; also `--exec`, `--ext-diff`, `--upload-pack`). |
| 4 | `sort -o /etc/hosts x`, `uniq in.txt out.txt`, `tree -o out.txt` | safe | confirm, or dangerous for system paths | "Read-only" tools that can write their output to a file named in an argument rather than through `>`. |

The general lesson: an allow-list of command *names* is not enough. A safe
command becomes unsafe through its **options** (`-c`, `--output`, `-o`) or its
**targets** (devices, credential files). The classifier now checks both.

A related design issue came up between two requirements: "`-f` skips the
confirm tier" and "reads of sensitive paths never run without confirmation".
`cat .env` is in the confirm tier, so `-f` would have run it silently.
Decisions now carry a `Sensitive` flag, and `-f` does not apply to them.

## Fuzzing

`internal/policy/fuzz_test.go` is a native Go fuzz test. On arbitrary input it
checks that:

- the lexer and classifier never panic;
- input the lexer cannot parse is never classified `safe`;
- no prefix can hide a dangerous command: `<fuzzed>; rm -rf ~` and
  `<fuzzed> && sudo x` are always `dangerous`. The only exceptions are
  prefixes that turn the suffix into inert text for the real shell too: an
  open quote, a trailing backslash, or a comment.

```bash
go test ./internal/policy -fuzz=FuzzClassify -fuzztime=60s
```

It has run for several million executions without a failure. An early version
of the test also exempted heredocs. That was wrong: in `cat <<EOF; rm -rf ~`
the heredoc body starts on the *next* line, so `rm` does run. The exemption
was removed, and the classifier already handled the case correctly.

## Known limitations

These are deliberate trade-offs, documented rather than hidden:

- **Not a sandbox.** Approved commands run as your user, with your
  permissions, on your machine. The evaluation harness runs each task in a
  fresh temporary directory, with a temporary `HOME`, API keys removed from
  the environment, and dangerous commands always denied. That is isolation,
  not containment: an absolute path still reaches the real filesystem.
- **Secrets in ordinary files.** Path patterns catch `.env`, keys, cloud and
  cluster config and shell history, but not a password inside `config.yaml`.
  A `safe` read of such a file is sent to the provider. Use a paid tier with a
  no-training data policy, or keep agent mode away from secret-bearing
  directories.
- **Free-tier data use.** On the Gemini free tier, Google may use prompts and
  responses to improve its products. Agent mode sends command output (logs,
  configs, diffs), so avoid sensitive data on the free tier.
- **The lexer is not a full POSIX parser.** It does not model heredoc bodies,
  arithmetic expansion or every bash extension. Each gap is handled by
  escalating (an unparsed construct is never `safe`), so the failure mode is
  an extra confirmation, not a silent run.
- **You are the last line of defence.** A confirmed command runs. Read
  `confirm` and `dangerous` commands before answering `y`.

## Reporting

If you find a command that is classified below its real risk, please open an
issue with the command and the expected tier, or add it to the table in
`internal/policy/policy_test.go`.
