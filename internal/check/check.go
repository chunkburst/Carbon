// Package check executes a task's checks as shell commands and records the outcome
// (SPEC §6). It is a leaf package: it knows nothing about tasks, config, or the store.
// Callers resolve a check into a Spec (command, cwd, timeout), Run it, and write the
// Result back. Manual checks (no command) are never passed here.
//
// Contract (SPEC §6): every command runs via `sh -c`, so POSIX shell is required;
// native Windows users use WSL/Git Bash, or set CARBON_SHELL to a shell on PATH. Exit
// code 0 is pass, non-zero is fail, a timeout is a (killed) fail. Combined stdout+stderr
// is tailed to a log under LogDir; the task file keeps only the result.
package check

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TailBytes is how much of the combined output is retained — the trailing ~8KB
// (SPEC §6). Older output is discarded as it streams, bounding memory on noisy checks.
const TailBytes = 8 << 10

// ErrCwdOutsideRoot is returned when a check's requested working directory escapes the
// runner root, including through a symlink.
var ErrCwdOutsideRoot = errors.New("check working directory must be within runner root")

// Spec is a single resolved check to execute. Cmd is a full shell command line. Cwd is
// relative to the runner's Root ("" or "." = root). Timeout <= 0 means no deadline; the
// caller is expected to have already applied config's check_timeout_default.
type Spec struct {
	Cmd     string
	Cwd     string
	Timeout time.Duration
}

// Result is the outcome of running a Spec. Pass is the only value the task file stores
// (as pass/fail); the rest aids diagnostics and the run log.
type Result struct {
	Pass     bool
	ExitCode int
	TimedOut bool
	Reason   string // non-empty on failure: exit status, timeout, or start error
	Output   string // trailing TailBytes of combined stdout+stderr
	LogPath  string // path of the written run log
	Duration time.Duration
	GitHead  string // commit HEAD observed when the run started, if available
}

// Runner executes checks rooted at a repo and writes logs under LogDir. LogRoot is the
// trusted containment root for logs; when empty it defaults to Root, preserving the
// canonical <root>/.carbon/runs layout. Carbon supplies its cluster data root as LogRoot so
// task metadata and run logs stay in the trusted data store while commands still execute
// in the project's source checkout. Now is an injectable clock for log filenames; nil
// uses the wall clock. The zero value is usable when Root/LogDir default to the process cwd.
type Runner struct {
	Root    string
	LogRoot string
	LogDir  string
	// LogWriteLock optionally serializes final log publication with a metadata
	// transaction. Carbon services bind it to Store.Write so a project clear cannot
	// race a foreign task's new run log. Command execution itself remains unlocked.
	LogWriteLock func(func() error) error
	Now          func() time.Time
	GitHead      string
	// Shell is the configured shell (config.yaml check_shell). Empty ⇒ sh. The CARBON_SHELL
	// env var overrides it; CAIRN_SHELL remains a legacy fallback.
	Shell string
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r Runner) logRoot() string {
	if r.LogRoot != "" {
		return r.LogRoot
	}
	return r.Root
}

// resolveShell returns the shell used to run checks, by precedence: CARBON_SHELL, the legacy
// CAIRN_SHELL env var, the configured Shell, then `sh`.
// then the configured Shell, then `sh`. On Windows only, an implicit default `sh` can fall
// back to a standard Git Bash install when PATH does not contain it. Explicit environment or
// config values never receive that fallback, so their existing error/override semantics hold.
func (r Runner) resolveShell() (string, error) {
	return resolveShell(configuredShellEnv(), r.Shell, exec.LookPath, defaultShellFallback)
}

func configuredShellEnv() string {
	if value := os.Getenv("CARBON_SHELL"); value != "" {
		return value
	}
	return os.Getenv("CAIRN_SHELL")
}

func resolveShell(envShell, configuredShell string, lookPath func(string) (string, error), fallback func() string) (string, error) {
	shell := envShell
	implicitDefault := false
	if shell == "" {
		shell = configuredShell
		if shell == "" {
			shell = "sh"
			implicitDefault = true
		}
	}
	if _, err := lookPath(shell); err == nil {
		return shell, nil
	} else {
		if implicitDefault {
			if resolved := fallback(); resolved != "" {
				return resolved, nil
			}
		}
		return "", fmt.Errorf("check: shell %q not found on PATH. Install a POSIX shell "+
			"(Git Bash or WSL on Windows), or set CARBON_SHELL to one: %w", shell, err)
	}
}

// Run executes spec via the resolved shell, captures the tail of its output, writes a run log, and
// reports the result. A non-zero exit or a timeout is a failed Result, not an error; the
// error return is reserved for infrastructure faults (e.g. the log could not be written).
func (r Runner) Run(id string, spec Spec) (Result, error) {
	return r.RunContext(context.Background(), id, spec)
}

// RunContext executes spec like Run, using ctx as the parent cancellation signal. When
// spec.Timeout is set, the command is canceled by whichever happens first: ctx or timeout.
func (r Runner) RunContext(ctx context.Context, id string, spec Spec) (Result, error) {
	if spec.Cmd == "" {
		return Result{}, errors.New("check: empty command (manual checks are not run by the runner)")
	}

	dir, err := r.resolveCwd(spec.Cwd)
	if err != nil {
		return Result{}, err
	}

	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	var timeoutCancel context.CancelFunc
	if spec.Timeout > 0 {
		runCtx, timeoutCancel = context.WithTimeout(runCtx, spec.Timeout)
		defer timeoutCancel()
	}

	shell, err := r.resolveShell()
	if err != nil {
		return Result{}, err
	}
	cmd := exec.CommandContext(runCtx, shell, "-c", spec.Cmd)
	cmd.Dir = dir
	// On a timeout/cancel, kill the command and its children, not just the sh that spawned
	// them. How that's done is OS-specific (POSIX process group vs. plain kill on Windows).
	configureKill(cmd)
	cmd.WaitDelay = 2 * time.Second

	out := &tailBuffer{max: TailBytes}
	cmd.Stdout = out
	cmd.Stderr = out

	start := r.now()
	runErr := cmd.Run()
	res := Result{Output: out.String(), Duration: r.now().Sub(start), GitHead: r.GitHead}

	switch {
	case runCtx.Err() == context.DeadlineExceeded:
		res.TimedOut = true
		res.ExitCode = -1
		res.Reason = fmt.Sprintf("timed out after %s", spec.Timeout)
	case runErr == nil:
		res.Pass = true
	default:
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			res.ExitCode = ee.ExitCode()
			res.Reason = fmt.Sprintf("exit status %d", res.ExitCode)
		} else {
			res.ExitCode = -1
			res.Reason = "failed to run: " + runErr.Error()
		}
	}

	logPath, err := r.writeLog(id, spec, dir, res)
	if err != nil {
		return res, err
	}
	res.LogPath = logPath
	return res, nil
}

// resolveCwd resolves Root and Cwd through symlinks before enforcing containment. Cwd is
// intentionally relative (per Spec); accepting an absolute path would make a typo or
// copied command silently execute outside the repository.
func (r Runner) resolveCwd(cwd string) (string, error) {
	root, err := existingDir(r.Root)
	if err != nil {
		return "", fmt.Errorf("check: resolve runner root: %w", err)
	}
	if filepath.IsAbs(cwd) {
		return "", fmt.Errorf("%w: %s", ErrCwdOutsideRoot, cwd)
	}
	path := root
	if cwd != "" && cwd != "." {
		path = filepath.Join(root, cwd)
	}
	dir, err := existingDir(path)
	if err != nil {
		return "", fmt.Errorf("check: resolve working directory %q: %w", cwd, err)
	}
	if !isWithinRoot(root, dir) {
		return "", fmt.Errorf("%w: %s", ErrCwdOutsideRoot, cwd)
	}
	return dir, nil
}

func existingDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("not a directory: %s", path)
	}
	return real, nil
}

func isWithinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (r Runner) writeLog(id string, spec Spec, dir string, res Result) (string, error) {
	if r.LogWriteLock == nil {
		return r.writeLogUnlocked(id, spec, dir, res)
	}
	var path string
	err := r.LogWriteLock(func() error {
		var writeErr error
		path, writeErr = r.writeLogUnlocked(id, spec, dir, res)
		return writeErr
	})
	return path, err
}

func (r Runner) writeLogUnlocked(id string, spec Spec, dir string, res Result) (string, error) {
	logDir, err := prepareRunLogDir(r.logRoot(), r.LogDir, true)
	if err != nil {
		return "", err
	}
	stamp := r.now().UTC().Format("20060102-150405.000")
	header := fmt.Sprintf("cmd: %s\ncwd: %s\nhead: %s\nexit: %d  timedout: %t  duration: %s\n----\n",
		spec.Cmd, dir, res.GitHead, res.ExitCode, res.TimedOut, res.Duration)
	for n := 0; n < 1000; n++ {
		suffix := ""
		if n > 0 {
			suffix = fmt.Sprintf("-%03d", n)
		}
		path := filepath.Join(logDir, fmt.Sprintf("%s-%s%s.log", id, stamp, suffix))
		if err := freshRunLogPath(logDir, path); err != nil {
			if errors.Is(err, errRunLogExists) {
				continue
			}
			return "", err
		}
		// O_EXCL makes the final create no-follow and no-clobber: a malicious link
		// (or another runner's log) that appears after freshRunLogPath is inspected
		// cannot be overwritten or followed.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("check: create run log: %w", err)
		}
		if _, err := f.Write([]byte(header + res.Output)); err != nil {
			_ = f.Close()
			return "", fmt.Errorf("check: write log: %w", err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("check: close log: %w", err)
		}
		return path, nil
	}
	return "", fmt.Errorf("check: could not allocate a unique run-log filename")
}

// tailBuffer is an io.Writer that retains only the trailing max bytes written to it.
// It is safe for the concurrent Stdout/Stderr writers os/exec spawns.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
