package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrderedKeys(t *testing.T) {
	keys, err := orderedKeys([]byte(`{"b": 1, "a": {"nested": true}, "c": [1,2]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"b", "a", "c"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", keys, want)
	}
}

func TestOrderedKeysRejectsNonObject(t *testing.T) {
	if _, err := orderedKeys([]byte(`[1,2]`)); err == nil {
		t.Fatal("expected error for non-object")
	}
}

func writeTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const validQuestions = `{
  "s": {"type": "score", "instructions": "x", "criteria": ["bad", "good"]},
  "c": {"type": "choice", "instructions": "x", "criteria": {"accept": "ok", "revise": "fix"}},
  "n": {"type": "noul", "instructions": "x"}
}`

func TestLoadQuestionsValid(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "q.json", validQuestions)
	raw, ids, err := loadQuestions(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "s,c,n" {
		t.Fatalf("ids out of order: %v", ids)
	}
	if len(raw) == 0 {
		t.Fatal("raw questions not preserved")
	}
}

func TestLoadQuestionsInvalid(t *testing.T) {
	cases := map[string]string{
		"bad type":                   `{"q": {"type": "ranking", "instructions": "x"}}`,
		"missing type":               `{"q": {"instructions": "x"}}`,
		"missing instructions":       `{"q": {"type": "score", "criteria": ["a", "b"]}}`,
		"empty instructions":         `{"q": {"type": "score", "instructions": "", "criteria": ["a", "b"]}}`,
		"score one level":            `{"q": {"type": "score", "instructions": "x", "criteria": ["only"]}}`,
		"score object criteria":      `{"q": {"type": "score", "instructions": "x", "criteria": {"a": "b"}}}`,
		"choice array criteria":      `{"q": {"type": "choice", "instructions": "x", "criteria": ["a", "b"]}}`,
		"choice empty":               `{"q": {"type": "choice", "instructions": "x", "criteria": {}}}`,
		"not an object":              `[1,2]`,
		"empty object":               `{}`,
		"numeric instructions":       `{"q": {"type": "score", "instructions": 42, "criteria": ["a", "b"]}}`,
		"whitespace instructions":    `{"q": {"type": "score", "instructions": "  ", "criteria": ["a", "b"]}}`,
		"score numeric levels":       `{"q": {"type": "score", "instructions": "x", "criteria": [1, 2]}}`,
		"choice numeric description": `{"q": {"type": "choice", "instructions": "x", "criteria": {"a": 1}}}`,
		"noul array criteria":        `{"q": {"type": "noul", "instructions": "x", "criteria": ["a"]}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeTemp(t, t.TempDir(), "q.json", body)
			if _, _, err := loadQuestions(path); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestGateOptionOrder(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "q.json", validQuestions)
	raw, _, err := loadQuestions(path)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := gateOptionOrder(raw, "c")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(opts, ",") != "accept,revise" {
		t.Fatalf("option order lost: %v", opts)
	}
	if _, err := gateOptionOrder(raw, "missing"); err == nil {
		t.Fatal("expected error for unknown gate question")
	}
	if _, err := gateOptionOrder(raw, "s"); err == nil {
		t.Fatal("expected error for non-choice gate question")
	}
}

func TestCollectCodeSkips(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "lib/keep.ex", "defmodule Keep do\nend\n")
	writeTemp(t, dir, "lib/.hidden.ex", "secret\n")
	writeTemp(t, dir, "node_modules/pkg/index.js", "junk\n")
	writeTemp(t, dir, "_build/out.beam", "junk\n")

	files, err := collectCode([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected exactly 1 file, got %d: %v", len(files), sortedKeys(files))
	}
	if _, ok := files[filepath.Join(dir, "lib/keep.ex")]; !ok {
		t.Fatalf("keep.ex missing: %v", sortedKeys(files))
	}
}

func TestCollectCodeExplicitFileAndErrors(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "one.ex", "code\n")
	files, err := collectCode([]string{path})
	if err != nil || len(files) != 1 {
		t.Fatalf("explicit file: files=%v err=%v", files, err)
	}
	if _, err := collectCode([]string{filepath.Join(dir, "nope.ex")}); err == nil {
		t.Fatal("expected error for missing path")
	}
	empty := t.TempDir()
	if _, err := collectCode([]string{empty}); err == nil {
		t.Fatal("expected error for directory with no readable files")
	}
}

func TestInSkippedDir(t *testing.T) {
	if !inSkippedDir("node_modules/pkg/index.js") {
		t.Fatal("node_modules should be skipped")
	}
	if !inSkippedDir("lib/_build/x.ex") {
		t.Fatal("_build should be skipped")
	}
	if inSkippedDir("lib/keep.ex") {
		t.Fatal("lib/keep.ex should not be skipped")
	}
	if inSkippedDir("deps.ex") {
		t.Fatal("a file named deps.ex is not the deps dir")
	}
}
