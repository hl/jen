package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// emptyTree is git's well-known empty-tree object, used as the diff base in
// repositories that have no commits yet.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// collectDiff gathers the working-tree diff against base plus the full
// current content of every changed or untracked file. Paths in the returned
// map are relative to the repository root. Untracked files appear in the
// diff as synthesized new-file hunks, so brand-new files are judged as part
// of the change. Files listed in exclude (any path form) are dropped from
// the result — used to keep the tool's own config file out of the review
// when it sits untracked inside the repo.
func collectDiff(base string, exclude ...string) (string, map[string]string, error) {
	if strings.HasPrefix(base, "-") {
		return "", nil, fmt.Errorf("invalid --base '%s': must be a git ref, not an option", base)
	}
	top, err := gitOutput("", "rev-parse", "--show-toplevel")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", nil, fmt.Errorf("git is not installed or not on PATH")
		}
		return "", nil, fmt.Errorf("not inside a git repository")
	}
	top = strings.TrimSpace(top)

	// A repo with no commits has no HEAD; diff against the empty tree so
	// brand-new repos still work.
	if _, err := gitOutput(top, "rev-parse", "--verify", "--quiet", base); err != nil {
		if base == "HEAD" {
			base = emptyTree
		} else {
			return "", nil, fmt.Errorf("unknown git ref '%s'", base)
		}
	}

	// Run from the repo root so diff paths are repo-relative. Force --no-color
	// so a color-forcing git config can't leak ANSI escapes into the state.
	diffText, err := gitOutput(top, "diff", "--no-color", base)
	if err != nil {
		return "", nil, fmt.Errorf("git diff against '%s' failed: %v", base, err)
	}

	// Changed files, excluding deletions (their content no longer exists).
	names, err := gitOutput(top, "diff", "--name-only", "-z", "--diff-filter=d", base, "--")
	if err != nil {
		return "", nil, fmt.Errorf("git diff against '%s' failed: %v", base, err)
	}
	// Untracked files (respecting .gitignore) never appear in git diff.
	untracked, err := gitOutput(top, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return "", nil, fmt.Errorf("git ls-files failed: %v", err)
	}

	excluded := map[string]bool{}
	// git reports the real (symlink-resolved) repo root — e.g. /tmp is a
	// symlink on macOS — so resolve exclusions the same way before comparing.
	realTop, err := filepath.EvalSymlinks(top)
	if err != nil {
		realTop = top
	}
	for _, p := range exclude {
		if abs, absErr := filepath.Abs(p); absErr == nil {
			if real, symErr := filepath.EvalSymlinks(abs); symErr == nil {
				abs = real
			}
			if rel, relErr := filepath.Rel(realTop, abs); relErr == nil {
				excluded[rel] = true
			}
		}
	}

	files := map[string]string{}
	for _, name := range append(splitPaths(names), splitPaths(untracked)...) {
		if excluded[name] || inSkippedDir(name) {
			continue
		}
		path := filepath.Join(top, name)
		info, statErr := os.Lstat(path)
		if statErr != nil {
			continue
		}
		// Git stores a symlink's target path, never its referent's content.
		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return "", nil, fmt.Errorf("could not read symlink %q: %w", name, readErr)
			}
			files[name] = target
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if text, readErr := readTextFile(path); readErr == nil {
			files[name] = text
		}
	}

	// Untracked files are absent from `git diff`; synthesize a new-file diff
	// for each so the change under review actually includes them.
	var extra strings.Builder
	for _, name := range splitPaths(untracked) {
		if excluded[name] || inSkippedDir(name) {
			continue
		}
		if _, ok := files[name]; !ok {
			continue // unreadable or binary
		}
		if d, diffErr := gitDiffNewFile(top, name); diffErr == nil {
			extra.WriteString(d)
		}
	}
	return diffText + extra.String(), files, nil
}

// inSkippedDir reports whether any directory component of a repo-relative
// path is a never-useful directory (node_modules, _build, ...).
func inSkippedDir(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts[:len(parts)-1] {
		if skipDirNames[part] {
			return true
		}
	}
	return false
}

// gitDiffNewFile returns a unified diff presenting the untracked file name
// as newly added. git diff --no-index exits 1 when differences exist, which
// is the expected case here, not an error.
func gitDiffNewFile(top, name string) (string, error) {
	cmd := exec.Command("git", "diff", "--no-color", "--no-index", "--", "/dev/null", name)
	cmd.Dir = top
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.ExitCode() == 1 {
			return string(out), nil
		}
		return "", err
	}
	return string(out), nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			detail := strings.TrimSpace(string(exitErr.Stderr))
			if detail != "" {
				return "", fmt.Errorf("%s", detail)
			}
		}
		return "", err
	}
	return string(out), nil
}

func splitPaths(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\x00"), "\x00")
}
