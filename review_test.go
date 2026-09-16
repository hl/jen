package main

import (
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectDiffPreservesFilenames(t *testing.T) {
	repo := initRepo(t)
	names := []string{"café.ex", " spaced.ex", "trailing.ex ", "tab\tfile.ex", "line\nfile.ex", "quote\"file.ex"}
	for _, name := range names {
		writeTemp(t, repo, name, "fresh\n")
	}
	for _, tracked := range []bool{false, true} {
		if tracked {
			if _, err := gitOutput(repo, "add", "--all"); err != nil {
				t.Fatal(err)
			}
		}
		diff, files, err := collectDiff("HEAD")
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if files[name] != "fresh\n" {
				t.Errorf("tracked=%v: missing %q", tracked, name)
			}
		}
		if strings.Count(diff, "+fresh") != len(names) {
			t.Errorf("tracked=%v: incomplete diff: %q", tracked, diff)
		}
	}
}

func TestCollectCodeDoesNotFollowSymlinks(t *testing.T) {
	outside := writeTemp(t, t.TempDir(), "outside.txt", "synthetic outside content")
	root := t.TempDir()
	writeTemp(t, root, "keep.go", "code")
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	files, err := collectCode([]string{root})
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", sortedKeys(files), err)
	}
	if _, err := collectCode([]string{link}); err == nil {
		t.Fatal("explicit symlink should require selecting its target directly")
	}
}

func TestCollectDiffRepresentsSymlinkWithoutReadingTarget(t *testing.T) {
	repo := initRepo(t)
	outside := writeTemp(t, t.TempDir(), "outside.txt", "synthetic outside content")
	if err := os.Symlink(outside, filepath.Join(repo, "link.txt")); err != nil {
		t.Fatal(err)
	}
	diff, files, err := collectDiff("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if files["link.txt"] != outside || strings.Contains(diff, "synthetic outside content") {
		t.Fatal("symlink should contribute only the target path")
	}
	if !strings.Contains(diff, "120000") {
		t.Fatal("symlink diff missing")
	}
}

func TestLoadStructuredQuestions(t *testing.T) {
	body := `{
	 "n":{"type":"noul","instructions":{"question":"correct?"},"criteria":{"true":["yes"],"false":null}},
	 "c":{"type":"choice","instructions":["select"],"criteria":{"pass":{"description":"good"},"fail":null}},
	 "s":{"type":"score","instructions":"rate","criteria":[{"description":"bad"},["good"]]},
	 "null":{"type":"noul","instructions":null}
	}`
	p := writeTemp(t, t.TempDir(), "q.json", body)
	raw, ids, err := loadQuestions(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != body || len(ids) != 4 {
		t.Fatal("questions were not preserved")
	}
	opts, err := gateOptionOrder(raw, "c")
	if err != nil || strings.Join(opts, ",") != "pass,fail" {
		t.Fatalf("gate order: %v, %v", opts, err)
	}
}

type responseTransport func(*http.Request) (*http.Response, error)

func (f responseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRunValidatesAllAnswers(t *testing.T) {
	root := t.TempDir()
	plan := writeTemp(t, root, "plan.md", "plan")
	code := writeTemp(t, root, "code.go", "code")
	questions := writeTemp(t, root, "q.json", validQuestions)
	valid := `{"model":"jev-latest","answers":{
	 "n":{"type":"noul","noul":0.9},
	 "c":{"type":"choice","choice":"accept","probabilities":{"accept":0.8,"revise":0.2},"confidence":0.6},
	 "s":{"type":"score","score":0.8,"legend":{"0":"bad","1":"good"},"probabilities":{"0":0.2,"1":0.8},"confidence":0.6}
	},"usage":{"input_tokens":1,"output_tokens":1}}`
	cases := map[string]string{
		"empty":                 "{}",
		"null":                  "null",
		"missing answer":        strings.Replace(valid, `"n":{"type":"noul","noul":0.9},`, "", 1),
		"missing value":         strings.Replace(valid, `"noul":0.9`, `"unused":0.9`, 1),
		"wrong type":            strings.Replace(valid, `"type":"noul"`, `"type":"score"`, 1),
		"out of range":          strings.Replace(valid, `"noul":0.9`, `"noul":1.9`, 1),
		"unknown choice":        strings.Replace(valid, `"choice":"accept"`, `"choice":"unknown"`, 1),
		"missing confidence":    strings.ReplaceAll(valid, `,"confidence":0.6`, ""),
		"missing probabilities": strings.Replace(valid, `"probabilities":{"accept":0.8,"revise":0.2},`, "", 1),
		"missing score":         strings.Replace(valid, `"score":0.8,`, "", 1),
		"invalid score":         strings.Replace(valid, `"score":0.8`, `"score":2`, 1),
		"missing legend":        strings.Replace(valid, `"legend":{"0":"bad","1":"good"},`, "", 1),
		"valid":                 valid,
		"structured legend":     strings.Replace(valid, `"0":"bad","1":"good"`, `"0":{"description":"bad"},"1":["good"]`, 1),
		"failed gate":           strings.Replace(valid, `"choice":"accept"`, `"choice":"revise"`, 1),
	}
	for _, gate := range []string{"", "c"} {
		for name, body := range cases {
			t.Run(name+"/gate="+gate, func(t *testing.T) {
				oldTransport, oldFlags, oldArgs, oldStdout := http.DefaultTransport, flag.CommandLine, os.Args, os.Stdout
				t.Cleanup(func() {
					http.DefaultTransport, flag.CommandLine, os.Args, os.Stdout = oldTransport, oldFlags, oldArgs, oldStdout
				})
				http.DefaultTransport = responseTransport(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
				})
				flag.CommandLine = flag.NewFlagSet("jen", flag.ContinueOnError)
				os.Args = []string{"jen", "--plan", plan, "--code", code, "--questions", questions}
				if gate != "" {
					os.Args = append(os.Args, "--gate", gate)
				}
				f, err := os.CreateTemp(t.TempDir(), "stdout")
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				os.Stdout = f
				t.Setenv(apiKeyEnv, "synthetic-test-key")
				want := 1
				if name == "valid" || name == "structured legend" || name == "failed gate" {
					want = 0
				}
				if name == "failed gate" && gate != "" {
					want = 3
				}
				if rc := run(); rc != want {
					t.Fatalf("exit=%d want=%d", rc, want)
				}
				data, err := os.ReadFile(f.Name())
				if err != nil {
					t.Fatal(err)
				}
				if want == 1 && len(data) != 0 {
					t.Fatal("invalid response emitted a success report")
				}
				if want != 1 && !json.Valid(data) {
					t.Fatal("valid response did not emit JSON")
				}
				if name == "structured legend" {
					var report Report
					if err := json.Unmarshal(data, &report); err != nil {
						t.Fatal(err)
					}
					if string(report.Summary["s"].Legend["0"]) == `"bad"` || !strings.Contains(string(report.Summary["s"].Legend["0"]), "description") {
						t.Fatal("structured legend was not preserved")
					}
					if !strings.Contains(renderFeedback(&report, []string{"s"}), "80%") || !strings.Contains(renderTable(&report, []string{"s"}), "80%") {
						t.Fatal("structured score was not normalized in text reports")
					}
				}
			})
		}
	}
}
