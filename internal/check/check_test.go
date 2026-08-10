package check

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runner(t *testing.T) Runner {
	t.Helper()
	root := t.TempDir()
	return Runner{Root: root, LogDir: filepath.Join(root, ".carbon", "runs")}
}

func TestRunPass(t *testing.T) {
	r := runner(t)
	r.GitHead = "abc123"
	res, err := r.Run("PROJ-001", Spec{Cmd: "exit 0"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Pass || res.ExitCode != 0 || res.TimedOut {
		t.Fatalf("got %+v, want pass exit 0", res)
	}
	if res.LogPath == "" {
		t.Fatal("expected a log path")
	}
	if _, err := os.Stat(res.LogPath); err != nil {
		t.Fatalf("log not written: %v", err)
	}
	b, err := os.ReadFile(res.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "head: abc123\n") {
		t.Fatalf("log missing git head:\n%s", b)
	}
}

func TestRunMissingShellIsClearError(t *testing.T) {
	t.Setenv("CARBON_SHELL", "carbon-no-such-shell-xyz")
	_, err := runner(t).Run("PROJ-001", Spec{Cmd: "exit 0"})
	if err == nil {
		t.Fatal("expected an error when the shell is missing")
	}
	if !strings.Contains(err.Error(), "CARBON_SHELL") {
		t.Errorf("error should mention CARBON_SHELL, got: %v", err)
	}
}

func TestResolveShellUsesFallbackOnlyForImplicitDefault(t *testing.T) {
	missing := errors.New("not found")
	var lookedUp []string
	calledFallback := false
	got, err := resolveShell("", "", func(name string) (string, error) {
		lookedUp = append(lookedUp, name)
		return "", missing
	}, func() string {
		calledFallback = true
		return `C:\Program Files\Git\bin\sh.exe`
	})
	if err != nil {
		t.Fatalf("resolveShell: %v", err)
	}
	if got != `C:\Program Files\Git\bin\sh.exe` || !calledFallback {
		t.Fatalf("resolveShell = %q, fallback called=%t", got, calledFallback)
	}
	if len(lookedUp) != 1 || lookedUp[0] != "sh" {
		t.Fatalf("lookups = %#v, want [sh]", lookedUp)
	}
}

func TestResolveShellDoesNotFallbackForExplicitOverride(t *testing.T) {
	missing := errors.New("not found")
	for _, tc := range []struct {
		name       string
		envShell   string
		configured string
		wantLookup string
	}{
		{name: "environment", envShell: "custom-shell", wantLookup: "custom-shell"},
		{name: "configured", configured: "custom-shell", wantLookup: "custom-shell"},
		{name: "configured sh", configured: "sh", wantLookup: "sh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calledFallback := false
			_, err := resolveShell(tc.envShell, tc.configured, func(name string) (string, error) {
				if name != tc.wantLookup {
					t.Errorf("lookup(%q), want %q", name, tc.wantLookup)
				}
				return "", missing
			}, func() string {
				calledFallback = true
				return `C:\Program Files\Git\bin\sh.exe`
			})
			if err == nil {
				t.Fatal("resolveShell unexpectedly succeeded")
			}
			if calledFallback {
				t.Fatal("resolveShell used the Git Bash fallback for an explicit shell")
			}
		})
	}
}

func TestRunUsesConfiguredShellWhenEnvUnset(t *testing.T) {
	t.Setenv("CARBON_SHELL", "") // env unset → fall through to Runner.Shell
	t.Setenv("CAIRN_SHELL", "")
	r := runner(t)
	r.Shell = "carbon-no-such-shell-xyz"
	_, err := r.Run("PROJ-001", Spec{Cmd: "exit 0"})
	if err == nil || !strings.Contains(err.Error(), "carbon-no-such-shell-xyz") {
		t.Fatalf("expected the configured shell to be used, got: %v", err)
	}
}

func TestEnvShellOverridesConfiguredShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	t.Setenv("CARBON_SHELL", sh) // env wins over a bogus configured shell
	r := runner(t)
	r.Shell = "carbon-no-such-shell-xyz"
	res, err := r.Run("PROJ-001", Spec{Cmd: "exit 0"})
	if err != nil || !res.Pass {
		t.Fatalf("env shell should win; got res=%+v err=%v", res, err)
	}
}

func TestConfiguredShellEnvPrefersCarbonAndKeepsLegacyFallback(t *testing.T) {
	t.Setenv("CARBON_SHELL", "carbon-shell")
	t.Setenv("CAIRN_SHELL", "legacy-shell")
	if got := configuredShellEnv(); got != "carbon-shell" {
		t.Fatalf("canonical shell env = %q", got)
	}
	t.Setenv("CARBON_SHELL", "")
	if got := configuredShellEnv(); got != "legacy-shell" {
		t.Fatalf("legacy shell fallback = %q", got)
	}
}

func TestRunHonorsCarbonShell(t *testing.T) {
	// Point CARBON_SHELL at the real sh by absolute path; checks must still run.
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	t.Setenv("CARBON_SHELL", sh)
	res, err := runner(t).Run("PROJ-001", Spec{Cmd: "exit 0"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Pass {
		t.Fatalf("got %+v, want pass", res)
	}
}

func TestRunFailExitCode(t *testing.T) {
	r := runner(t)
	res, err := r.Run("PROJ-001", Spec{Cmd: "exit 7"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Pass || res.ExitCode != 7 {
		t.Fatalf("got %+v, want fail exit 7", res)
	}
	if res.Reason == "" {
		t.Fatal("expected a reason on failure")
	}
}

func TestRunCapturesStdoutAndStderr(t *testing.T) {
	r := runner(t)
	res, err := r.Run("PROJ-001", Spec{Cmd: "echo out; echo err 1>&2"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Output, "out") || !strings.Contains(res.Output, "err") {
		t.Fatalf("output %q missing stdout/stderr", res.Output)
	}
	// The log file on disk contains the captured output.
	b, err := os.ReadFile(res.LogPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(b), "out") {
		t.Fatalf("log %q missing output", b)
	}
}

func TestRunCwdRelativeToRoot(t *testing.T) {
	r := runner(t)
	if err := os.MkdirAll(filepath.Join(r.Root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := r.Run("PROJ-001", Spec{Cmd: "pwd", Cwd: "sub"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.HasSuffix(strings.TrimSpace(res.Output), "sub") {
		t.Fatalf("pwd %q did not run in sub", res.Output)
	}
}

func TestRunRejectsCwdOutsideRoot(t *testing.T) {
	r := runner(t)
	if _, err := r.Run("PROJ-001", Spec{Cmd: "exit 0", Cwd: ".."}); !errors.Is(err, ErrCwdOutsideRoot) {
		t.Fatalf("Run outside root = %v, want ErrCwdOutsideRoot", err)
	}
}

func TestRunRejectsSymlinkCwdOutsideRoot(t *testing.T) {
	r := runner(t)
	link := filepath.Join(r.Root, "escape")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := r.Run("PROJ-001", Spec{Cmd: "exit 0", Cwd: "escape"}); !errors.Is(err, ErrCwdOutsideRoot) {
		t.Fatalf("Run symlink outside root = %v, want ErrCwdOutsideRoot", err)
	}
}

func TestRunTimeoutKills(t *testing.T) {
	r := runner(t)
	start := time.Now()
	res, err := r.Run("PROJ-001", Spec{Cmd: "sleep 5", Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.TimedOut || res.Pass {
		t.Fatalf("got %+v, want timed out", res)
	}
	if !strings.Contains(strings.ToLower(res.Reason), "tim") {
		t.Fatalf("reason %q does not mention timeout", res.Reason)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took %s, process not killed promptly", elapsed)
	}
}

func TestRunContextCancellationKills(t *testing.T) {
	r := runner(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := r.RunContext(ctx, "PROJ-001", Spec{Cmd: "sleep 5"})
	if err != nil {
		t.Fatalf("RunContext: %v", err)
	}
	if res.Pass {
		t.Fatalf("got %+v, want canceled failure", res)
	}
}

func TestRunTailTruncatesLog(t *testing.T) {
	r := runner(t)
	res, err := r.Run("PROJ-001", Spec{Cmd: "for i in $(seq 1 20000); do echo line$i; done"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Output) > TailBytes {
		t.Fatalf("output kept %d bytes, want <= %d", len(res.Output), TailBytes)
	}
}

func TestRunEmptyCmdIsError(t *testing.T) {
	r := runner(t)
	if _, err := r.Run("PROJ-001", Spec{Cmd: ""}); err == nil {
		t.Fatal("expected error for empty cmd (manual checks are not run here)")
	}
}

func TestRunAllowsSeparateTrustedLogRoot(t *testing.T) {
	// Carbon's project checkout and its cluster data store can be separate
	// directories (including separate Windows volumes). The command must keep its
	// source-root cwd while the log is contained by the independently trusted
	// data-root boundary.
	sourceRoot := t.TempDir()
	dataRoot := t.TempDir()
	r := Runner{
		Root:    sourceRoot,
		LogRoot: dataRoot,
		LogDir:  filepath.Join(dataRoot, ".carbon", "runs"),
	}

	res, err := r.Run("PROJ-001", Spec{Cmd: "pwd"})
	if err != nil {
		t.Fatalf("Run across separate roots: %v", err)
	}
	if !res.Pass {
		t.Fatalf("Run across separate roots = %+v, want pass", res)
	}
	if !isWithinRoot(dataRoot, res.LogPath) {
		t.Fatalf("log path = %q, want within trusted data root %q", res.LogPath, dataRoot)
	}
	if isWithinRoot(sourceRoot, res.LogPath) {
		t.Fatalf("log path = %q unexpectedly within source root %q", res.LogPath, sourceRoot)
	}
	if !strings.Contains(res.Output, filepath.Base(sourceRoot)) {
		t.Fatalf("command cwd %q did not remain in source root %q", res.Output, sourceRoot)
	}
}

func TestRunRejectsLogDirOutsideExplicitTrustedLogRoot(t *testing.T) {
	sourceRoot := t.TempDir()
	dataRoot := t.TempDir()
	outside := t.TempDir()
	logDir := filepath.Join(outside, ".carbon", "runs")
	r := Runner{Root: sourceRoot, LogRoot: dataRoot, LogDir: logDir}

	_, err := r.Run("PROJ-001", Spec{Cmd: "exit 0"})
	if !errors.Is(err, ErrLogDirOutsideRoot) {
		t.Fatalf("Run with log dir outside explicit trusted root = %v, want ErrLogDirOutsideRoot", err)
	}
	if _, statErr := os.Stat(logDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("untrusted log directory was created: %v", statErr)
	}
}

func TestRunRejectsRunLogDirectorySymlinkOutsideRoot(t *testing.T) {
	r := runner(t)
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(r.LogDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, r.LogDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := r.Run("PROJ-001", Spec{Cmd: "exit 0"})
	if !errors.Is(err, ErrUnsafeRunLogPath) {
		t.Fatalf("Run with symlinked log directory = %v, want ErrUnsafeRunLogPath", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("outside directory received a run log: %+v", entries)
	}
}

func TestRunRejectsSymlinkedRunLog(t *testing.T) {
	r := runner(t)
	at := time.Date(2026, time.June, 21, 19, 0, 0, 0, time.UTC)
	r.Now = func() time.Time { return at }
	if _, err := prepareRunLogDir(r.Root, r.LogDir, true); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("do not overwrite"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "PROJ-001-" + at.Format("20060102-150405.000") + ".log"
	if err := os.Symlink(outside, filepath.Join(r.LogDir, name)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := r.Run("PROJ-001", Spec{Cmd: "exit 0"})
	if !errors.Is(err, ErrUnsafeRunLogPath) {
		t.Fatalf("Run with symlinked log = %v, want ErrUnsafeRunLogPath", err)
	}
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "do not overwrite" {
		t.Fatalf("outside log changed to %q", got)
	}
}

func TestRunKeepsDistinctLogsAtSameMillisecond(t *testing.T) {
	r := runner(t)
	at := time.Date(2026, time.June, 21, 19, 0, 0, 0, time.UTC)
	r.Now = func() time.Time { return at }

	first, err := r.Run("PROJ-001", Spec{Cmd: "exit 0"})
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	second, err := r.Run("PROJ-001", Spec{Cmd: "exit 0"})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if first.LogPath == second.LogPath {
		t.Fatalf("same log path reused: %s", first.LogPath)
	}
	if !strings.HasSuffix(second.LogPath, "-001.log") {
		t.Fatalf("second log path = %q, want collision suffix", second.LogPath)
	}
}
