// Package process wraps os/exec with a fluent builder modelled on
// Laravel's Process facade.
//
// Compared to os/exec, the wrapper:
//   - returns a typed Result with ExitCode, Stdout, Stderr, Duration;
//   - accepts a context.Context for cancellation and timeouts;
//   - never starts a shell — the first argument is the program, the
//     rest are exact argv entries (no string-split surprises, no
//     injection from interpolated values).
//
// Usage:
//
//	r, err := process.Command("git", "rev-parse", "HEAD").Run(ctx)
//	if err != nil { return err }
//	fmt.Println(r.Stdout)
//
//	// timeout + working directory + extra env
//	_, _ = process.Command("npm", "test").
//	    Dir("/srv/app").
//	    Env("CI", "1").
//	    Timeout(2 * time.Minute).
//	    Run(ctx)
package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// Result captures the outcome of a process invocation.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

// Successful reports whether the process exited with code 0.
func (r Result) Successful() bool { return r.ExitCode == 0 }

// ErrTimeout is returned when the process is killed because the
// configured timeout (or the caller's context deadline) elapsed.
var ErrTimeout = errors.New("process: timeout")

// Cmd is an immutable builder. Each setter returns a clone so chains
// compose safely:
//
//	base := process.Command("git").Dir("/repo")
//	a := base.Args("status", "--short")  // doesn't mutate base
type Cmd struct {
	name    string
	args    []string
	dir     string
	env     map[string]string
	timeout time.Duration
	stdin   io.Reader
}

// Command builds a process invocation. The first argument is the
// program; remaining arguments are passed as separate argv entries
// (no shell interpretation).
func Command(name string, args ...string) *Cmd {
	return &Cmd{name: name, args: append([]string{}, args...), env: map[string]string{}}
}

func (c *Cmd) clone() *Cmd {
	cp := *c
	cp.args = append([]string{}, c.args...)
	cp.env = make(map[string]string, len(c.env))
	for k, v := range c.env {
		cp.env[k] = v
	}
	return &cp
}

// Args replaces the argument list.
func (c *Cmd) Args(args ...string) *Cmd {
	cp := c.clone()
	cp.args = append([]string{}, args...)
	return cp
}

// AppendArgs adds to the existing argument list.
func (c *Cmd) AppendArgs(args ...string) *Cmd {
	cp := c.clone()
	cp.args = append(cp.args, args...)
	return cp
}

// Dir sets the working directory.
func (c *Cmd) Dir(d string) *Cmd {
	cp := c.clone()
	cp.dir = d
	return cp
}

// Env adds an environment variable (in addition to the parent process
// environment).
func (c *Cmd) Env(k, v string) *Cmd {
	cp := c.clone()
	cp.env[k] = v
	return cp
}

// Timeout caps the wall-clock duration. 0 means no per-process timeout
// (the parent context still applies).
func (c *Cmd) Timeout(d time.Duration) *Cmd {
	cp := c.clone()
	cp.timeout = d
	return cp
}

// Stdin pipes r into the process stdin.
func (c *Cmd) Stdin(r io.Reader) *Cmd {
	cp := c.clone()
	cp.stdin = r
	return cp
}

// Run executes the command and waits for it to finish. The returned
// error is non-nil for non-zero exit codes, transport failures, or
// timeouts; the Result is still populated when an exit code is known.
func (c *Cmd) Run(ctx context.Context) (Result, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, c.name, c.args...)

	// Place the child in its own process group (POSIX) so the Cancel hook
	// can kill the whole tree — not just the direct child. Without this a
	// child that backgrounds a grandchild (e.g. `sh -c "sleep 30 & wait"`)
	// leaves the grandchild holding the stdout/stderr pipe open, and Wait
	// blocks until the grandchild exits even though the child was killed.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessTree(cmd) }

	// WaitDelay bounds how long Wait blocks after the process is killed
	// waiting for inherited I/O fds to close. Without it, a leaked
	// grandchild holding the pipe keeps Run blocked for the grandchild's
	// full lifetime. After the delay, exec force-closes the I/O and Wait
	// returns. (Go 1.20+.)
	cmd.WaitDelay = 2 * time.Second

	if c.dir != "" {
		cmd.Dir = c.dir
	}
	if len(c.env) > 0 {
		// Inherit parent env first, append overrides — exec deduplicates
		// by keeping the latter when keys repeat.
		cmd.Env = append([]string{}, mergedEnv(c.env)...)
	}
	if c.stdin != nil {
		cmd.Stdin = c.stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	res := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: dur,
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		// CommandContext kills the process when ctx is done. Map this
		// back to ErrTimeout so callers don't need to inspect ctx.Err.
		if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
			return res, ErrTimeout
		}
		// exec.ExitError carries the non-zero exit code via Result.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return res, &ExitError{Result: res, Err: err}
		}
		return res, fmt.Errorf("process: run %s: %w", c.name, err)
	}
	return res, nil
}

// ExitError wraps a non-zero exit so callers can inspect Result fields
// while still using errors.As.
type ExitError struct {
	Result Result
	Err    error
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("process: exit %d: %s", e.Result.ExitCode, strings.TrimSpace(e.Result.Stderr))
}

func (e *ExitError) Unwrap() error { return e.Err }

// mergedEnv combines the parent environment with overrides. Later
// entries override earlier ones in exec semantics.
func mergedEnv(overrides map[string]string) []string {
	parent := osEnviron()
	merged := make([]string, 0, len(parent)+len(overrides))
	merged = append(merged, parent...)
	for k, v := range overrides {
		merged = append(merged, k+"="+v)
	}
	return merged
}
