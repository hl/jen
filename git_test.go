package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a git repo in dir with one committed file and returns the
// repo path. Skips the test if git is unavailable.
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t.t")
	run("config", "user.name", "t")
	writeTemp(t, dir, "tracked.ex", "old\n")
	run("add", "-A")
	run("commit", "-qm", "init")
	chdir(t, dir)
	return dir
}

// chdir switches the process working directory for the duration of the
// test; collectDiff resolves the repository from the cwd, as in production.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}

func TestCollectDiffModifiedAndUntracked(t *testing.T) {
	repo := initRepo(t)
	writeTemp(t, repo, "tracked.ex", "new\n")
	writeTemp(t, repo, "brand_new.ex", "fresh\n")

	diff, files, err := collectDiff("HEAD", filepath.Join(repo, ".jen.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tracked.ex", "brand_new.ex"} {
		if _, ok := files[name]; !ok {
			t.Fatalf("%s missing from files: %v", name, files)
		}
	}
	if !strings.Contains(diff, "-old") || !strings.Contains(diff, "+new") {
		t.Fatalf("modified file diff missing:\n%s", diff)
	}
	// Untracked files must appear in the diff as new-file hunks.
	if !strings.Contains(diff, "brand_new.ex") || !strings.Contains(diff, "+fresh") {
		t.Fatalf("untracked file missing from diff:\n%s", diff)
	}
}

func TestCollectDiffCleanTree(t *testing.T) {
	initRepo(t)
	diff, files, err := collectDiff("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(diff) != "" || len(files) != 0 {
		t.Fatalf("clean tree should yield nothing, got diff=%q files=%v", diff, files)
	}
}

func TestCollectDiffNoCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	writeTemp(t, dir, "first.ex", "brand new repo\n")
	chdir(t, dir)
	diff, files, err := collectDiff("HEAD") // HEAD does not exist yet
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["first.ex"]; !ok {
		t.Fatalf("untracked file missing: %v", files)
	}
	if !strings.Contains(diff, "+brand new repo") {
		t.Fatalf("expected new-file diff, got:\n%s", diff)
	}
}

func TestCollectDiffBadBase(t *testing.T) {
	initRepo(t)
	if _, _, err := collectDiff("no-such-ref"); err == nil {
		t.Fatal("expected error for unknown ref")
	}
	if _, _, err := collectDiff("--cached"); err == nil {
		t.Fatal("expected error for option-like base")
	}
}

func TestCollectDiffExcludesConfig(t *testing.T) {
	repo := initRepo(t)
	cfg := writeTemp(t, repo, ".jen.json", "{}\n")
	_, files, err := collectDiff("HEAD", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("config file should be excluded, got %v", files)
	}
}

func TestCollectDiffSkipsHeavyDirs(t *testing.T) {
	repo := initRepo(t)
	writeTemp(t, repo, "node_modules/pkg/index.js", "junk\n")
	_, files, err := collectDiff("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("node_modules should be skipped, got %v", files)
	}
}

func TestCollectDiffOutsideRepo(t *testing.T) {
	chdir(t, t.TempDir())
	if _, _, err := collectDiff("HEAD"); err == nil {
		t.Fatal("expected error outside a git repository")
	}
}
