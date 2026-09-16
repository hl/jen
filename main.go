// Command jen evaluates code against a plan using the TypeSafe System One
// API.
//
// Built for agent-driven workflows: an agent writes code from a plan, then
// runs this CLI to get structured, typed judgments about the result. The
// plan and code are sent as named state fields ("plan" and "code") and every
// question in the questions file is asked in a single API request.
//
// Usage:
//
//	jen --plan plan.md --code lib/foo.ex --questions questions.json
//	jen --plan plan.md --diff --questions questions.json
//	jen --config .jen.json --format feedback
//
// Diff mode (--diff): validate the git working-tree diff against --base
// (default HEAD) plus untracked files, instead of --code paths. The state
// then carries an extra "diff" field with the unified diff, and "code"
// holds the full current content of just the changed files.
//
// Questions file format (JSON map of question id -> TypeSafe question):
//
//	{
//	  "plan_satisfaction": {
//	    "type": "score",
//	    "instructions": "How well does the code in `code` satisfy `plan`?",
//	    "criteria": ["Ignores the plan", "...", "Fully implements the plan"]
//	  },
//	  "overall_verdict": {
//	    "type": "choice",
//	    "instructions": "What should happen to this code?",
//	    "criteria": {"accept": "Ready as-is", "revise": "Needs changes"}
//	  }
//	}
//
// Question instructions can reference the state with backticked paths:
// `plan` is the plan text, `code` is a map of file path -> source, and
// `diff` (diff mode only) is the unified diff of the change under review.
//
// Gating (--gate QUESTION_ID): point --gate at a choice question to make the
// exit code actionable. The option order in the question's criteria defines
// severity: the FIRST option means "pass". The exit code is 0 when the first
// option is chosen, otherwise 2 + the chosen option's index. The JSON report
// always goes to stdout regardless of exit code, so callers can branch on the
// exit code and still parse stdout.
//
// Config file (--config FILE): a JSON object supplying defaults for plan,
// code, diff, base, questions, gate, model, format, output, and timeout.
// CLI flags override config values. Relative paths inside the config resolve
// against the config file's directory.
//
// Exit codes:
//
//	0 = evaluation succeeded (and gate passed, if --gate was given)
//	1 = runtime/API error
//	2 = usage error (bad flags, missing required inputs)
//	3+ = gate failed; 2 + index of the chosen gate option
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"unicode/utf8"
)

const usage = `jen — validate code against a plan using the TypeSafe System One API

WHAT JEN DOES
  Built for agent-driven workflows: a coding agent writes code from a
  plan, then runs jen to get structured, typed judgments about the
  result. The plan and code are sent as named state fields and every
  question in the questions file is asked in a single API request
  (parallel, cheap, fast).

INPUT MODES (pick one)
  --diff      RECOMMENDED for agents. Validates the git working tree
              against --base (default HEAD), including untracked files.
              The state carries "diff" (unified diff) and "code" (full
              content of exactly the changed files), so judgments focus
              on what changed, not the whole repo. Clean tree = exit 0
              with a skip note, no API call.
  --code PATH Validate explicit files or directories. Repeatable flag
              (one value per flag). Use outside git or for greenfield
              code not yet committed.

QUESTIONS FILE (JSON map of question id -> question)
  {
    "plan_satisfaction": {
      "type": "score",
      "instructions": "How well does ` + "`diff`" + ` satisfy ` + "`plan`" + `?",
      "criteria": ["Ignores the plan", "...", "Fully implements the plan"]
    },
    "idiomatic": {
      "type": "score",
      "instructions": "How idiomatic is the code in ` + "`code`" + `?",
      "criteria": ["Not idiomatic", "...", "Fully idiomatic"]
    },
    "plan_deviation": {
      "type": "noul",
      "instructions": "Does the implementation deviate from ` + "`plan`" + `?"
    },
    "overall_verdict": {
      "type": "choice",
      "instructions": "What should happen to this code?",
      "criteria": {"accept": "Ready as-is", "revise": "Needs changes"}
    }
  }

  Question types: "score" (graded rubric; criteria is an array of level
  descriptions from worst to best), "noul" (yes/no probability), and
  "choice" (pick one option; criteria is option -> description, where the
  option ORDER matters for --gate). Instructions reference state fields
  with backticked paths: ` + "`plan`" + ` = plan text, ` + "`code`" + ` = map of file
  path -> source, ` + "`diff`" + ` = unified diff (diff mode only). A complete,
  mode-aware example ships in the repo at examples/questions.elixir.json.

GATES & EXIT CODES
  --gate QUESTION_ID points at a choice question and makes the exit code
  actionable: exit 0 when the FIRST option in its criteria is chosen,
  otherwise 2 + the chosen option's index. The JSON report always goes
  to stdout regardless of exit code, so callers can branch on the exit
  code and still parse stdout. Reports also carry a "summary"; treat any
  answer with confidence < 0.5 as inconclusive, not pass/fail.

    0 = evaluation succeeded (gate passed, if --gate given)
    1 = runtime/API error
    2 = usage error (bad flags, missing required inputs)
    3+ = gate failed; 2 + index of the chosen gate option

HOOK MODE (--hook) — for agent lifecycle hooks
  Wraps a run for use as a blocking hook (e.g. Claude Code Stop hooks):
  gate failures are re-mapped to exit 2 with feedback on stderr, while
  infrastructure errors (missing API key, bad paths, API failures) exit
  0 with a warning so they never block the agent. Output defaults to the
  feedback format on stderr.

  Claude Code (.claude/settings.json):
    {
      "hooks": {
        "Stop": [{
          "matcher": "",
          "hooks": [{"type": "command", "command": "jen --hook --config .jen.json"}]
        }]
      }
    }

  Other agents: run jen normally and branch on the exit code, or use
  --hook to get short, actionable feedback text instead of JSON.

CONFIG FILE (--config FILE)
  A JSON object supplying defaults for: plan, code, diff, base,
  questions, gate, model, format, output, timeout. CLI flags override
  config values. Relative paths inside the config resolve against the
  config file's directory. Example (diff workflow):
    {
      "plan": "plan.md",
      "diff": true,
      "base": "main",
      "questions": "questions.json",
      "gate": "overall_verdict"
    }

ENVIRONMENT
  TYPESAFE_API_KEY   required.

EXAMPLES
  # Validate the working tree diff and gate on a verdict question:
  jen --plan plan.md --diff --questions questions.json --gate overall_verdict

  # Validate explicit files with a human-readable table:
  jen --plan plan.md --code lib/ --questions questions.json --format table

  # Config-driven run saving the full JSON report:
  jen --config .jen.json --output report.json

FLAGS`

const (
	defaultModel    = "jev-latest"
	apiKeyEnv       = "TYPESAFE_API_KEY"
	defaultTimeout  = 120.0
	lowConfidenceAt = 0.5
)

// version is stamped at build time: go build -ldflags "-X main.version=x.y.z"
var version = "dev"

// Directories never worth sending as code context.
var skipDirNames = map[string]bool{
	".git": true, "_build": true, "deps": true, "node_modules": true,
	".elixir_ls": true, "dist": true, "__pycache__": true,
}

// stringList collects repeated --code flags.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// config mirrors the JSON config file; CLI flags override it.
type config struct {
	Plan      string   `json:"plan"`
	Code      []string `json:"code"`
	Diff      bool     `json:"diff"`
	Base      string   `json:"base"`
	Questions string   `json:"questions"`
	Gate      string   `json:"gate"`
	Model     string   `json:"model"`
	Format    string   `json:"format"`
	Output    string   `json:"output"`
	Timeout   *float64 `json:"timeout"`
}

func main() {
	os.Exit(run())
}

func run() int {
	var codeFlags stringList
	var cfgPath, plan, questions, gate, model, format, output, base string
	var timeout float64
	var showVersion, diffMode, hookMode bool

	flag.Var(&codeFlags, "code", "code file or directory; repeatable")
	flag.BoolVar(&diffMode, "diff", false, "validate the git working-tree diff against --base, plus untracked files")
	flag.StringVar(&base, "base", "", "git ref to diff against (requires --diff; default HEAD)")
	flag.StringVar(&cfgPath, "config", "", "JSON config file supplying defaults; CLI flags override")
	flag.StringVar(&plan, "plan", "", "path to the plan document (text/markdown)")
	flag.StringVar(&questions, "questions", "", "path to the questions JSON file")
	flag.StringVar(&gate, "gate", "", "choice question whose answer sets the exit code")
	flag.StringVar(&model, "model", "", "model to use (default "+defaultModel+")")
	flag.StringVar(&format, "format", "", "output format: json, table, feedback (default json)")
	flag.StringVar(&output, "output", "", "also write the JSON report to this file")
	flag.Float64Var(&timeout, "timeout", 0, "request timeout in seconds (default 120)")
	flag.BoolVar(&hookMode, "hook", false, "hook mode for agent lifecycle hooks: feedback on stderr, gate failure exits 2, infra errors exit 0")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), usage)
		fmt.Fprintln(flag.CommandLine.Output(), " (all inputs also via --config):")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() > 0 {
		return usageErrorf("unexpected arguments: %s (all inputs are passed via flags)", strings.Join(flag.Args(), " "))
	}

	if showVersion {
		printVersion()
		return 0
	}

	// Track which flags were explicitly set so config values only fill gaps.
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })

	// Load config file, if any.
	var cfg config
	cfgDir := "."
	if cfgPath != "" {
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			return usageErrorf("could not read config '%s': %v", cfgPath, err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return usageErrorf("config '%s' is not valid JSON: %v", cfgPath, err)
		}
		abs, err := filepath.Abs(cfgPath)
		if err == nil {
			cfgDir = filepath.Dir(abs)
		}
	}

	// Merge: explicit flag > config > default.
	if !set["plan"] {
		plan = cfg.Plan
	}
	if !set["code"] {
		codeFlags = cfg.Code
	}
	if !set["diff"] {
		diffMode = cfg.Diff
	}
	if !set["base"] {
		base = cfg.Base
	}
	if !set["questions"] {
		questions = cfg.Questions
	}
	if !set["gate"] {
		gate = cfg.Gate
	}
	if !set["model"] {
		model = cfg.Model
	}
	if !set["format"] {
		format = cfg.Format
	}
	if !set["output"] {
		output = cfg.Output
	}
	if !set["timeout"] && cfg.Timeout != nil {
		timeout = *cfg.Timeout
	}
	if model == "" {
		model = defaultModel
	}
	if format == "" {
		format = "json"
		if hookMode {
			format = "feedback" // hooks want actionable text by default
		}
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	// An explicit --code or --diff flag wins over the config's mode setting,
	// per the "CLI flags override config" contract.
	if set["code"] && !set["diff"] {
		diffMode = false
	}
	if set["diff"] && !set["code"] {
		codeFlags = nil
	}
	hookActive = hookMode
	if format != "json" && format != "table" && format != "feedback" {
		return usageErrorf("invalid --format '%s': expected json, table, or feedback", format)
	}
	if diffMode && len(codeFlags) > 0 {
		return usageErrorf("--diff and --code are mutually exclusive")
	}
	if set["base"] && !diffMode {
		return usageErrorf("--base requires --diff")
	}
	if !diffMode {
		base = "" // a config-supplied base is meaningless outside diff mode
	}
	if diffMode && base == "" {
		base = "HEAD"
	}

	// Resolve config-relative paths against the config file's directory.
	resolve := func(p string, fromConfig bool) string {
		if fromConfig && !filepath.IsAbs(p) {
			return filepath.Join(cfgDir, p)
		}
		return p
	}
	planFromCfg := !set["plan"] && cfg.Plan != ""
	codeFromCfg := !set["code"] && len(cfg.Code) > 0
	questionsFromCfg := !set["questions"] && cfg.Questions != ""
	outputFromCfg := !set["output"] && cfg.Output != ""

	var missing []string
	if plan == "" {
		missing = append(missing, "--plan")
	}
	if len(codeFlags) == 0 && !diffMode {
		missing = append(missing, "--code (or --diff)")
	}
	if questions == "" {
		missing = append(missing, "--questions")
	}
	if len(missing) > 0 {
		return usageErrorf("missing required inputs: %s (supply as flags or via --config)",
			strings.Join(missing, ", "))
	}

	apiKey := os.Getenv(apiKeyEnv)
	if apiKey == "" {
		return runtimeErrorf("%s is not set in the environment", apiKeyEnv)
	}

	planText, err := readTextFile(resolve(plan, planFromCfg))
	if err != nil {
		return runtimeErrorf("could not read plan file: %v", err)
	}

	mode := "files"
	var codeFiles map[string]string
	var diffText string
	if diffMode {
		mode = "diff"
		// Keep the tool's own config file out of the review when it sits
		// untracked inside the repo being validated.
		var exclude []string
		if cfgPath != "" {
			exclude = append(exclude, cfgPath)
		}
		diffText, codeFiles, err = collectDiff(base, exclude...)
		if err != nil {
			return runtimeErrorf("%v", err)
		}
		if strings.TrimSpace(diffText) == "" && len(codeFiles) == 0 {
			note := fmt.Sprintf("no changes detected against %s; nothing to validate", base)
			skipReport := map[string]any{
				"mode": mode, "note": note, "files": []string{},
				"summary": map[string]any{}, "answers": map[string]any{},
			}
			data, _ := json.MarshalIndent(skipReport, "", "  ")
			if output != "" {
				if err := os.WriteFile(resolve(output, outputFromCfg), append(data, '\n'), 0o644); err != nil {
					return runtimeErrorf("could not write report to '%s': %v", output, err)
				}
			}
			if format == "json" {
				fmt.Println(string(data))
			} else {
				fmt.Println("PLAN VALIDATION SKIPPED: " + note)
			}
			return 0
		}
	} else {
		codePaths := make([]string, len(codeFlags))
		for i, p := range codeFlags {
			codePaths[i] = resolve(p, codeFromCfg)
		}
		codeFiles, err = collectCode(codePaths)
		if err != nil {
			return runtimeErrorf("%v", err)
		}
	}
	questionsPath := resolve(questions, questionsFromCfg)
	questionsRaw, orderedIDs, err := loadQuestions(questionsPath)
	if err != nil {
		return runtimeErrorf("%v", err)
	}

	var gateOptions []string
	if gate != "" {
		gateOptions, err = gateOptionOrder(questionsRaw, gate)
		if err != nil {
			return runtimeErrorf("%v", err)
		}
	}

	state := map[string]any{"plan": planText, "code": codeFiles}
	if diffMode {
		state["diff"] = diffText
	}
	payload, err := json.Marshal(map[string]any{
		"state": state,
		"model": model,
		// Forward the questions file verbatim, preserving key order.
		"questions": questionsRaw,
	})
	if err != nil {
		return runtimeErrorf("could not build request: %v", err)
	}

	apiResp, err := callAPI(apiKey, payload, timeout)
	if err != nil {
		return runtimeErrorf("%v", err)
	}
	if err := validateAnswers(apiResp, questionsRaw); err != nil {
		return runtimeErrorf("invalid API response: %v", err)
	}

	report := buildReport(apiResp, codeFiles, orderedIDs)
	report.Mode = mode

	exitCode := 0
	if gate != "" {
		answer, ok := apiResp.Answers[gate]
		if !ok || answer.Choice == "" {
			return runtimeErrorf("gate question '%s' returned no choice answer", gate)
		}
		idx := indexOf(gateOptions, answer.Choice)
		if idx < 0 {
			return runtimeErrorf("gate answer '%s' is not one of the options %v", answer.Choice, gateOptions)
		}
		if idx > 0 {
			exitCode = 2 + idx
		}
		report.Gate = &GateResult{
			Question:    gate,
			Choice:      answer.Choice,
			Options:     gateOptions,
			OptionIndex: idx,
			Passed:      idx == 0,
			Confidence:  answer.Confidence,
			ExitCode:    exitCode,
		}
	}

	if output != "" {
		data, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(resolve(output, outputFromCfg), append(data, '\n'), 0o644); err != nil {
			return runtimeErrorf("could not write report to '%s': %v", output, err)
		}
	}

	out := os.Stdout
	if hookActive {
		out = os.Stderr // hooks read feedback from stderr; stdout stays pipeline-clean
	}
	switch format {
	case "table":
		fmt.Fprintln(out, renderTable(report, orderedIDs))
	case "feedback":
		fmt.Fprintln(out, renderFeedback(report, orderedIDs))
	default:
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
	}
	// Hook mode re-maps gate failures to a single blocking code and infra
	// errors to a non-blocking pass, per hook convention.
	if hookActive && exitCode > 0 {
		return 2
	}
	return exitCode
}

func printVersion() {
	fmt.Printf("jen %s\n", version)
	if info, ok := debug.ReadBuildInfo(); ok {
		rev, dirty := "", ""
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					dirty = " (dirty)"
				}
			}
		}
		if rev != "" {
			fmt.Printf("commit: %s%s\n", rev, dirty)
		}
		fmt.Printf("go: %s\n", info.GoVersion)
	}
}

// hookActive enables hook-mode remapping of exits and output routing.
var hookActive bool

func usageErrorf(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	if hookActive {
		return 0 // never block hooks on tool misconfiguration
	}
	return 2
}

func runtimeErrorf(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	if hookActive {
		return 0
	}
	return 1
}

// ---------------------------------------------------------------------------
// Input collection
// ---------------------------------------------------------------------------

func readTextFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("'%s' is not valid UTF-8 text", path)
	}
	return string(data), nil
}

// collectCode gathers file path -> source for every supplied file or
// directory. Explicit files must be readable; unreadable files found while
// walking directories are skipped, as are symlinks and special files.
// Explicit symlinks are rejected; select their target paths directly.
func collectCode(paths []string) (map[string]string, error) {
	files := map[string]string{}
	for _, raw := range paths {
		info, err := os.Lstat(raw)
		if err != nil {
			return nil, fmt.Errorf("code path not found: %s", raw)
		}
		if !info.IsDir() {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("code path must be a regular file or directory: %s (select symlink targets directly)", raw)
			}
			text, err := readTextFile(raw)
			if err != nil {
				return nil, fmt.Errorf("could not read code file: %v", err)
			}
			files[raw] = text
			continue
		}
		root := raw
		err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable entries
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return nil
			}
			if rel != "." {
				parts := strings.Split(rel, string(filepath.Separator))
				for _, part := range parts {
					if strings.HasPrefix(part, ".") || skipDirNames[part] {
						if d.IsDir() {
							return filepath.SkipDir
						}
						return nil
					}
				}
			}
			if d.IsDir() {
				return nil
			}
			// Do not follow links or open devices/pipes discovered by a walk.
			if !d.Type().IsRegular() {
				return nil
			}
			if text, readErr := readTextFile(path); readErr == nil {
				files[path] = text
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("could not walk '%s': %v", raw, err)
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no readable code files found in the supplied --code paths")
	}
	return files, nil
}

// ---------------------------------------------------------------------------
// Questions file
// ---------------------------------------------------------------------------

// questionMeta is the validated view of one question; the raw bytes are what
// actually get forwarded to the API.
type questionMeta struct {
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria"`
}

// loadQuestions reads and validates the questions file, returning the raw
// object (for verbatim forwarding) and the question ids in file order.
func loadQuestions(path string) (json.RawMessage, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("could not read questions file '%s': %v", path, err)
	}
	raw := json.RawMessage(data)
	var questions map[string]json.RawMessage
	if err := json.Unmarshal(data, &questions); err != nil {
		return nil, nil, fmt.Errorf("questions file '%s' must be a JSON object of question id -> question: %v", path, err)
	}
	if len(questions) == 0 {
		return nil, nil, fmt.Errorf("questions file '%s' must contain at least one question", path)
	}
	orderedIDs, err := orderedKeys(data)
	if err != nil {
		return nil, nil, fmt.Errorf("questions file '%s' must be a JSON object: %v", path, err)
	}
	for _, qid := range orderedIDs {
		var meta questionMeta
		if err := json.Unmarshal(questions[qid], &meta); err != nil {
			return nil, nil, fmt.Errorf("question '%s' must be an object: %v", qid, err)
		}
		switch meta.Type {
		case "noul", "choice", "score":
		default:
			return nil, nil, fmt.Errorf("question '%s' has invalid type '%s'; expected noul, choice, or score", qid, meta.Type)
		}
		if !validEntry(meta.Instructions) {
			return nil, nil, fmt.Errorf("question '%s' needs 'instructions' as a string, object, array, or null", qid)
		}
		var instructions string
		if json.Unmarshal(meta.Instructions, &instructions) == nil && string(meta.Instructions) != "null" && strings.TrimSpace(instructions) == "" {
			return nil, nil, fmt.Errorf("question '%s' instructions must not be an empty string", qid)
		}
		switch meta.Type {
		case "score":
			var levels []json.RawMessage
			if err := json.Unmarshal(meta.Criteria, &levels); err != nil || len(levels) < 2 {
				return nil, nil, fmt.Errorf("score question '%s' needs a 'criteria' array of at least 2 level descriptions", qid)
			}
			for _, level := range levels {
				if !validEntry(level) {
					return nil, nil, fmt.Errorf("score question '%s' has an invalid level description", qid)
				}
			}
		case "choice":
			var options map[string]json.RawMessage
			if err := json.Unmarshal(meta.Criteria, &options); err != nil || len(options) == 0 {
				return nil, nil, fmt.Errorf("choice question '%s' needs a 'criteria' object of option -> description", qid)
			}
			for _, description := range options {
				if !validEntry(description) {
					return nil, nil, fmt.Errorf("choice question '%s' has an invalid option description", qid)
				}
			}
		case "noul":
			if len(meta.Criteria) > 0 {
				var criteria map[string]json.RawMessage
				if err := json.Unmarshal(meta.Criteria, &criteria); err != nil {
					return nil, nil, fmt.Errorf("noul question '%s' criteria, if present, must be an object with 'true'/'false' descriptions", qid)
				}
				for key, description := range criteria {
					if (key != "true" && key != "false") || !validEntry(description) {
						return nil, nil, fmt.Errorf("noul question '%s' has invalid criteria", qid)
					}
				}
			}
		}
	}
	return raw, orderedIDs, nil
}

// EntryType permits structured rubrics; keep the original JSON for forwarding.
func validEntry(raw json.RawMessage) bool {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch value.(type) {
	case nil, string, map[string]any, []any:
		return true
	default:
		return false
	}
}

// gateOptionOrder validates the gate question and returns its choice options
// in file order (order defines severity: first option = pass).
func gateOptionOrder(questions json.RawMessage, gate string) ([]string, error) {
	var byID map[string]json.RawMessage
	if err := json.Unmarshal(questions, &byID); err != nil {
		return nil, err
	}
	raw, ok := byID[gate]
	if !ok {
		return nil, fmt.Errorf("gate question '%s' not found in questions file", gate)
	}
	var meta questionMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, err
	}
	if meta.Type != "choice" {
		return nil, fmt.Errorf("gate question '%s' must be a choice question, got '%s'", gate, meta.Type)
	}
	return orderedKeys(meta.Criteria)
}

// orderedKeys returns the top-level keys of a JSON object in document order.
func orderedKeys(raw json.RawMessage) ([]string, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("expected a JSON object")
	}
	var keys []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		keys = append(keys, keyTok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

func indexOf(list []string, value string) int {
	for i, v := range list {
		if v == value {
			return i
		}
	}
	return -1
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
