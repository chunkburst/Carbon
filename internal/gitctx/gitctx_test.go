package gitctx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const helperMarkerEnv = "CARBON_GITCTX_HELPER_MARKER"

func TestParseNameStatusHandlesSpacesAndRenames(t *testing.T) {
	raw := []byte("M\x00web/src/App.tsx\x00A\x00docs/new guide.md\x00R100\x00old name.go\x00new name.go\x00")

	got := parseNameStatus(raw)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3: %+v", len(got), got)
	}
	if got[0] != (ChangedFile{Status: "M", Path: "web/src/App.tsx"}) {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1] != (ChangedFile{Status: "A", Path: "docs/new guide.md"}) {
		t.Fatalf("second = %+v", got[1])
	}
	if got[2] != (ChangedFile{Status: "R100", OldPath: "old name.go", Path: "new name.go"}) {
		t.Fatalf("rename = %+v", got[2])
	}
}

func TestParseStatusRenameOrientation(t *testing.T) {
	// porcelain -z emits renames as "XY NEW\0OLD" — the reverse of diff --name-status.
	raw := []byte("R  renamed.txt\x00orig.txt\x00 M web/src/App.tsx\x00")

	got := parseStatus(raw)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if got[0] != (ChangedFile{Status: "R", OldPath: "orig.txt", Path: "renamed.txt"}) {
		t.Fatalf("rename = %+v", got[0])
	}
	if got[1] != (ChangedFile{Status: "M", Path: "web/src/App.tsx"}) {
		t.Fatalf("modified = %+v", got[1])
	}
}

func TestSplitNULDropsTrailingEmpty(t *testing.T) {
	got := splitNUL([]byte("a\x00b\x00"))
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("split = %#v", got)
	}
}

func TestSafeGitArgsDisableHelpers(t *testing.T) {
	got := safeGitArgs("/repo", []string{"clean-driver", "process-driver"}, "diff", "--name-status")
	want := []string{
		"--no-pager",
		"-c", "core.fsmonitor=",
		"-c", "diff.external=",
		"-C", "/repo",
		"-c", "filter.clean-driver.clean=",
		"-c", "filter.clean-driver.smudge=",
		"-c", "filter.clean-driver.process=",
		"-c", "filter.clean-driver.required=false",
		"-c", "filter.process-driver.clean=",
		"-c", "filter.process-driver.smudge=",
		"-c", "filter.process-driver.process=",
		"-c", "filter.process-driver.required=false",
		"diff", "--name-status",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("safeGitArgs = %#v, want %#v", got, want)
	}
}

func TestFilterDriverForKey(t *testing.T) {
	for _, test := range []struct {
		key    string
		driver string
		ok     bool
	}{
		{key: "filter.lfs.clean", driver: "lfs", ok: true},
		{key: "filter.my.driver.process", driver: "my.driver", ok: true},
		{key: "filter.Case-Sensitive.smudge", driver: "Case-Sensitive", ok: true},
		{key: "filter.lfs.required"},
		{key: "diff.lfs.textconv"},
		{key: "filter..clean"},
	} {
		driver, ok := filterDriverForKey(test.key)
		if driver != test.driver || ok != test.ok {
			t.Errorf("filterDriverForKey(%q) = %q, %t; want %q, %t", test.key, driver, ok, test.driver, test.ok)
		}
	}
}

func TestSafeGitEnvStripsInheritedGitOverrides(t *testing.T) {
	got := safeGitEnv([]string{
		"PATH=/bin",
		"GIT_DIR=/outside",
		"git_work_tree=/outside",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_GLOBAL=/unsafe/global.gitconfig",
		"HOME=/home/test",
	})
	joined := strings.Join(got, "\n")
	for _, key := range []string{"GIT_DIR=", "git_work_tree=", "GIT_CONFIG_COUNT="} {
		if strings.Contains(joined, key) {
			t.Fatalf("safeGitEnv retained inherited %s: %#v", key, got)
		}
	}
	for _, entry := range []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_EXTERNAL_DIFF=",
		"GIT_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0",
	} {
		if !slices.Contains(got, entry) {
			t.Errorf("safeGitEnv missing %q: %#v", entry, got)
		}
	}
	if slices.Contains(got, "GIT_CONFIG_GLOBAL=/unsafe/global.gitconfig") {
		t.Fatalf("safeGitEnv retained inherited global config: %#v", got)
	}
}

func TestDiffIgnoresConfiguredExternalHelper(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "--quiet")
	path := filepath.Join(repo, "changed.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "--", "changed.txt")
	runGit(t, repo, "-c", "user.name=Carbon Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "-m", "initial")
	if err := os.WriteFile(path, []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Without --no-ext-diff / the forced empty diff.external this would try to invoke
	// a program that does not exist and fail the read-only context request.
	runGit(t, repo, "config", "diff.external", "carbon-missing-external-diff-helper")

	changed, err := Diff(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatalf("Diff with configured external helper: %v", err)
	}
	if len(changed) != 1 || changed[0] != (ChangedFile{Status: "M", Path: "changed.txt"}) {
		t.Fatalf("Diff = %+v", changed)
	}
}

func TestCallerRevisionCannotBecomeGitOption(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "--quiet")
	path := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(path, []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "--", "tracked.txt")
	runGit(t, repo, "-c", "user.name=Carbon Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "-m", "initial")

	for name, call := range map[string]func(context.Context, string, string) error{
		"diff": func(ctx context.Context, repo, rev string) error {
			_, err := Diff(ctx, repo, rev)
			return err
		},
		"log": func(ctx context.Context, repo, rev string) error {
			_, err := Log(ctx, repo, rev)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "must-not-be-created")
			err := call(context.Background(), repo, "--output="+output)
			if err == nil {
				t.Fatal("caller revision was accepted as a Git option")
			}
			if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("caller revision created Git output %q: %v", output, statErr)
			}
		})
	}
}

func TestGitReadsDoNotRunConfiguredHelpers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "--quiet")

	files := []string{
		"repo-clean.txt",
		"repo-process.txt",
		"global-clean.txt",
		"global-process.txt",
		"repo-diff.txt",
		"global-diff.txt",
	}
	attributes := strings.Join([]string{
		"repo-clean.txt filter=repo-clean",
		"repo-process.txt filter=repo-process",
		"global-clean.txt filter=global-clean",
		"global-process.txt filter=global-process",
		"repo-diff.txt diff=repo-diff",
		"global-diff.txt diff=global-diff",
		"",
	}, "\n")
	writeGitTestFile(t, filepath.Join(repo, ".gitattributes"), attributes)
	for _, name := range files {
		writeGitTestFile(t, filepath.Join(repo, name), "before\n")
	}
	runGit(t, repo, "add", "--", ".gitattributes")
	addArgs := append([]string{"add", "--"}, files...)
	runGit(t, repo, addArgs...)
	runGit(t, repo, "-c", "user.name=Carbon Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "-m", "initial")
	for _, name := range files {
		writeGitTestFile(t, filepath.Join(repo, name), "after\n")
	}

	marker := filepath.Join(t.TempDir(), "helper-ran")
	t.Setenv(helperMarkerEnv, marker)
	runGit(t, repo, "config", "filter.repo-clean.clean", helperCommand("repo-clean"))
	runGit(t, repo, "config", "diff.repo-diff.textconv", helperCommand("repo-diff"))

	globalConfig := filepath.Join(t.TempDir(), "global.gitconfig")
	runGit(t, repo, "config", "--file", globalConfig, "filter.global-clean.clean", helperCommand("global-clean"))
	runGit(t, repo, "config", "--file", globalConfig, "diff.global-diff.textconv", helperCommand("global-diff"))
	runGit(t, repo, "config", "--file", globalConfig, "core.fsmonitor", helperCommand("global-fsmonitor"))
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)

	// Prove the fixture is an actual external-helper risk before asserting that
	// the package's read-only Git commands suppress it.
	runGitAllowFailure(t, repo, "status", "--porcelain=v1", "-z")
	requireHelperMarker(t, marker, "unsafe git status")
	clearHelperMarker(t, marker)
	runGitAllowFailure(t, repo, "diff", "--textconv", "HEAD", "--")
	requireHelperMarker(t, marker, "unsafe git diff --textconv")
	clearHelperMarker(t, marker)

	// Long-running filters take precedence over clean filters. Add both local and
	// global process commands after the unsafe fixture check: the safe wrapper must
	// suppress them without invoking either helper.
	runGit(t, repo, "config", "filter.repo-process.process", helperCommand("repo-process"))
	runGit(t, repo, "config", "--file", globalConfig, "filter.global-process.process", helperCommand("global-process"))

	ref, err := Current(context.Background(), repo)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if ref.Head == "" {
		t.Fatal("Current returned an empty HEAD")
	}
	status, err := Status(context.Background(), repo)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	requireChangedPaths(t, status, "repo-clean.txt", "repo-process.txt", "global-clean.txt", "global-process.txt")
	changed, err := Diff(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	requireChangedPaths(t, changed, "repo-diff.txt", "global-diff.txt")
	commits, err := Log(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 1 || commits[0].Subject != "initial" {
		t.Fatalf("Log = %+v, want initial commit", commits)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("safe Git reads ran configured helper %q: %v", marker, err)
	}
}

func helperCommand(label string) string {
	return "printf '%s\\n' " + label + " >> \"$" + helperMarkerEnv + "\"; cat"
}

func writeGitTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGitAllowFailure(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	_ = cmd.Run()
}

func requireHelperMarker(t *testing.T, marker, command string) {
	t.Helper()
	if contents, err := os.ReadFile(marker); err != nil || len(contents) == 0 {
		t.Fatalf("%s did not run the configured helper: %v", command, err)
	}
}

func clearHelperMarker(t *testing.T, marker string) {
	t.Helper()
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
}

func requireChangedPaths(t *testing.T, files []ChangedFile, paths ...string) {
	t.Helper()
	changed := make(map[string]bool, len(files))
	for _, file := range files {
		changed[file.Path] = true
	}
	for _, path := range paths {
		if !changed[path] {
			t.Fatalf("changed files = %+v, want %q", files, path)
		}
	}
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
