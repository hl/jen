# AGENTS.md

Guidance for AI coding agents working in this repository.

## Project overview

`jen` is a single-package Go CLI (`module jen`, `package main`) that validates
code against a plan using the TypeSafe System One API. It has **no external
dependencies** — only the Go standard library. Keep it that way unless there
is a compelling reason to add a dependency.

## Repository layout

| File              | Responsibility                                                        |
|-------------------|-----------------------------------------------------------------------|
| `main.go`         | CLI entry point, flag/config parsing, input collection, questions-file validation, gate logic. The package doc comment and `usage` const are the canonical user docs — keep them in sync with behavior. |
| `api.go`          | System One API client: request/response types, retries/backoff, and strict answer validation. |
| `git.go`          | `--diff` mode: working-tree diff collection, untracked-file handling, exclusion logic. |
| `report.go`       | Report model and the `json`/`table`/`feedback` renderers.             |
| `*_test.go`       | Unit and integration tests (`main_test.go`, `git_test.go`, `review_test.go`). |
| `examples/`       | Runnable example: plan, questions file, and Elixir code under review. |
| `Makefile`        | `build`, `install`, `check`, `test`, `test-examples`, `clean`.        |

## Build and test

```sh
make check          # go vet + build — run before every commit
make test           # go test ./...
make build          # produces ./jen with version stamped
make test-examples  # requires Elixir; only needed when touching examples/
```

There is no CI; you are the CI. Always run `make check` and `make test` and
report the results.

## Invariants that must not break

These behaviors are contractual — callers (including other agents) depend on
them. Changing any of them is a breaking change.

1. **Exit codes** (`main.go`): 0 = success, 1 = runtime/API error, 2 = usage
   error, 3+ = gate failure (`2 + index` of the chosen gate option, where the
   first option is "pass").
2. **The JSON report always goes to stdout**, regardless of exit code, so
   callers can branch on the exit code and still parse stdout. (Exception:
   in hook mode, output is routed to stderr — see invariant 4.)
3. **Gate semantics**: `--gate` must point at a `choice` question; the order
   of options in its `criteria` object defines severity, first = pass.
4. **Hook mode** (`--hook`): gate failures re-map to exit 2 with feedback on
   stderr; infrastructure errors (missing API key, bad paths, API failures)
   exit 0 so they never block the agent. Output defaults to `feedback` on
   stderr.
5. **Questions are forwarded verbatim** to the API (raw JSON, key order
   preserved); local validation in `loadQuestions` must stay in sync with the
   three question types (`score`, `noul`, `choice`).
6. **Answer validation** (`api.go` `validateAnswers`) runs before any report
   or gate result is emitted: a syntactically valid JSON body is not an
   evaluation. Probabilities must sum to 1; scores/confidences must be in
   range.
7. **Config precedence**: explicit CLI flag > config file > default. Relative
   paths in the config resolve against the config file's directory.
8. **Diff mode** never follows symlinks, skips `skipDirNames` directories,
   and synthesizes new-file diff hunks for untracked files so they are judged
   as part of the change.

## Conventions

- Standard Go style: `gofmt`-clean, `go vet`-clean, doc comments on exported
  and non-obvious declarations.
- Errors are reported via `usageErrorf` (exit 2) vs `runtimeErrorf` (exit 1)
  depending on whether the user can fix it by changing inputs.
- The `jen` binary at the repo root is a build artifact — never commit
  changes to it.
- Version is stamped at build time via `-ldflags "-X main.version=..."`
  (see `Makefile`); do not hardcode versions in source.

## Using jen on itself

This repo can be validated with jen itself (dogfooding). With a plan file and
questions file present:

```sh
jen --plan plan.md --diff --questions questions.json --gate overall_verdict
```

Treat answers with confidence < 0.5 as inconclusive, not pass/fail. A gate
failure means: fix the weakest dimensions in the report, then re-run before
marking work complete.
