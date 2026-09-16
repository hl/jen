# jen

`jen` is a command-line tool that validates code against a plan using the
[TypeSafe System One](https://typesafe.ai) API. It is built for agent-driven
workflows: a coding agent writes code from a plan, then runs `jen` to get
structured, typed judgments about the result — scores, yes/no probabilities,
and verdicts — in a single API request.

## Installation

Requires Go 1.26+ (see `go.mod`) and git (for `--diff` mode).

```sh
git clone https://github.com/hl/jen.git
cd jen
make install   # or: make build  (produces ./jen)
```

Set your API key:

```sh
export TYPESAFE_API_KEY=...
```

## Quick start

```sh
# Validate the working-tree diff against HEAD and gate on a verdict question:
jen --plan plan.md --diff --questions questions.json --gate overall_verdict

# Validate explicit files or directories instead:
jen --plan plan.md --code lib/ --questions questions.json --format table

# Drive everything from a config file:
jen --config .jen.json --output report.json
```

A complete, runnable example (plan, questions file, and Elixir code) lives in
[`examples/`](examples/).

## How it works

1. **Inputs.** You provide a plan document (`--plan`), the code under review,
   and a questions file (`--questions`). Code comes from either:
   - `--diff` — the git working-tree diff against `--base` (default `HEAD`),
     plus untracked files. The API state carries a `diff` field (unified diff)
     and a `code` field (full current content of exactly the changed files).
     A clean tree exits 0 with a skip note and makes no API call.
   - `--code PATH` — explicit files or directories (repeatable). Use outside
     git or for code not yet committed.
2. **Questions.** The questions file is a JSON map of question id to a typed
   question. All questions are answered in one API request. Instructions
   reference the state with backticked paths: `` `plan` ``, `` `code` ``, and
   `` `diff` `` (diff mode only).
3. **Report.** The JSON report always goes to stdout (even when a gate
   fails; in hook mode output goes to stderr instead). Top-level fields:
   `model`, `mode`, `files` (paths reviewed), `usage` (token counts),
   `summary` (normalized, agent-friendly answers), `answers` (raw API
   answers), an optional `gate` result, and an optional `note` (e.g. the
   clean-tree skip message). `--format table` and `--format feedback` give
   human/agent-readable text; `--output FILE` also writes the JSON report
   to disk.

### Question types

| Type     | `criteria`                                              | Answer                                  |
|----------|---------------------------------------------------------|-----------------------------------------|
| `score`  | Array of level descriptions, worst to best (2+)         | Numeric score, legend, confidence       |
| `noul`   | Optional `{"true": ..., "false": ...}` descriptions     | Probability of "yes" (no confidence)    |
| `choice` | Object of option -> description; **order matters**      | Chosen option, probabilities, confidence |

Treat any answer with confidence < 0.5 as inconclusive, not pass/fail.

Confidence measures how concentrated the option probabilities are; it is
not the probability that the chosen answer is correct. For example, a score
split between two neighboring good levels can have low confidence even
though both levels describe acceptable code. See TypeSafe's
[confidence documentation](https://docs.typesafe.ai/confidence).

`--format feedback` shows option probabilities alongside each Choice and
Score, and labels a gate below 0.5 confidence `INCONCLUSIVE`. Individual
judgments remain visible: uncertainty about the overall verdict does not
make every dimension uncertain. Exit codes and JSON `gate.passed` still
reflect the selected option, independently of confidence; callers must
inspect confidence before deciding whether to act, including in hook mode.

## Gates and exit codes

`--gate QUESTION_ID` points at a `choice` question and makes the exit code
actionable. The option order in the question's criteria defines severity: the
**first** option means "pass".

| Exit code | Meaning                                              |
|-----------|------------------------------------------------------|
| 0         | Evaluation succeeded (and gate passed, if given)     |
| 1         | Runtime/API error                                    |
| 2         | Usage error (bad flags, missing required inputs)     |
| 3+        | Gate failed; `2 + index` of the chosen gate option   |

Because the JSON report always goes to stdout, callers can branch on the exit
code and still parse stdout.

## Hook mode

`--hook` wraps a run for use as a blocking agent lifecycle hook (e.g. Claude
Code Stop hooks): gate failures are re-mapped to exit 2 with feedback on
stderr, while infrastructure errors (missing API key, bad paths, API
failures) exit 0 with a warning so they never block the agent. Output
defaults to the `feedback` format on stderr.

```jsonc
// .claude/settings.json
{
  "hooks": {
    "Stop": [{
      "matcher": "",
      "hooks": [{"type": "command", "command": "jen --hook --config .jen.json"}]
    }]
  }
}
```

## Config file

`--config FILE` reads a JSON object supplying defaults; CLI flags override
config values. Relative paths inside the config resolve against the config
file's directory.

```json
{
  "plan": "plan.md",
  "diff": true,
  "base": "main",
  "questions": "questions.json",
  "gate": "overall_verdict"
}
```

Supported keys: `plan`, `code`, `diff`, `base`, `questions`, `gate`, `model`,
`format`, `output`, `timeout`.

## Flags

| Flag          | Description                                                        |
|---------------|-------------------------------------------------------------------|
| `--plan`      | Path to the plan document (text/markdown)                          |
| `--diff`      | Validate the git working-tree diff against `--base`                |
| `--base`      | Git ref to diff against (requires `--diff`; default `HEAD`)        |
| `--code`      | Code file or directory; repeatable                                 |
| `--questions` | Path to the questions JSON file                                    |
| `--gate`      | Choice question whose answer sets the exit code                    |
| `--config`    | JSON config file supplying defaults; CLI flags override            |
| `--model`     | Model to use (default `jev-latest`)                                |
| `--format`    | Output format: `json`, `table`, `feedback` (default `json`)        |
| `--output`    | Also write the JSON report to this file                            |
| `--timeout`   | Request timeout in seconds (default 120)                           |
| `--hook`      | Hook mode for agent lifecycle hooks                                |
| `--version`   | Print version and exit                                             |

## Development

```sh
make check          # go vet + build
make test           # go test ./...
make test-examples  # run the Elixir example tests (requires Elixir)
```

The `jen` binary at the repo root is a build artifact; it is gitignored
(`.gitignore`) and never committed.
