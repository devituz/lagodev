package process

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func skipIfWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix-specific test")
	}
}

func TestRun_EchoStdout(t *testing.T) {
	skipIfWindows(t)
	r, err := Command("sh", "-c", "echo hello").Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !r.Successful() {
		t.Fatalf("exit code = %d", r.ExitCode)
	}
	if strings.TrimSpace(r.Stdout) != "hello" {
		t.Fatalf("stdout = %q", r.Stdout)
	}
	if r.Duration <= 0 {
		t.Fatalf("duration must be > 0, got %v", r.Duration)
	}
}

func TestRun_StderrCaptured(t *testing.T) {
	skipIfWindows(t)
	r, _ := Command("sh", "-c", "echo oops 1>&2").Run(context.Background())
	if strings.TrimSpace(r.Stderr) != "oops" {
		t.Fatalf("stderr = %q", r.Stderr)
	}
}

func TestRun_NonZeroExit(t *testing.T) {
	skipIfWindows(t)
	r, err := Command("sh", "-c", "exit 42").Run(context.Background())
	if err == nil {
		t.Fatal("non-zero exit must return error")
	}
	if r.ExitCode != 42 {
		t.Fatalf("exit code = %d, want 42", r.ExitCode)
	}
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("err must wrap *ExitError, got %T", err)
	}
	if ee.Result.ExitCode != 42 {
		t.Fatalf("ExitError.Result.ExitCode = %d", ee.Result.ExitCode)
	}
}

func TestRun_Timeout(t *testing.T) {
	skipIfWindows(t)
	start := time.Now()
	_, err := Command("sh", "-c", "sleep 5").Timeout(50 * time.Millisecond).Run(context.Background())
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("process not killed promptly after timeout")
	}
}

func TestRun_ContextCancel(t *testing.T) {
	skipIfWindows(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := Command("sh", "-c", "sleep 5").Run(ctx)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout from cancelled context, got %v", err)
	}
}

// TestRun_TimeoutKillsBackgroundedGrandchild guards the root-cause fix:
// a child that backgrounds a grandchild (which inherits the stdout/stderr
// pipe) must not keep Run blocked for the grandchild's full lifetime.
// Setpgid + a process-group kill reaps the whole tree so Run returns
// promptly with ErrTimeout.
func TestRun_TimeoutKillsBackgroundedGrandchild(t *testing.T) {
	skipIfWindows(t)
	start := time.Now()
	// `sleep 30 &` backgrounds a grandchild holding the inherited pipe;
	// `wait` keeps the shell alive so the pipe stays open until the
	// grandchild dies.
	_, err := Command("sh", "-c", "sleep 30 & wait").
		Timeout(200 * time.Millisecond).
		Run(context.Background())
	elapsed := time.Since(start)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	// Generous bound: WaitDelay alone caps this at ~2s; the group kill
	// makes it sub-second. Anything near 30s means the tree leaked.
	if elapsed > 5*time.Second {
		t.Fatalf("Run blocked %v after timeout — grandchild not reaped", elapsed)
	}
}

func TestRun_StdinPipedIn(t *testing.T) {
	skipIfWindows(t)
	r, err := Command("cat").Stdin(strings.NewReader("piped\n")).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(r.Stdout) != "piped" {
		t.Fatalf("stdout = %q", r.Stdout)
	}
}

func TestRun_Dir(t *testing.T) {
	skipIfWindows(t)
	dir := t.TempDir()
	r, err := Command("pwd").Dir(dir).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// macOS resolves /tmp via /private/tmp — check suffix
	if !strings.HasSuffix(strings.TrimSpace(r.Stdout), dir) &&
		!strings.HasSuffix(strings.TrimSpace(r.Stdout), "/private"+dir) {
		t.Fatalf("pwd = %q, want suffix %q", r.Stdout, dir)
	}
}

func TestRun_EnvOverride(t *testing.T) {
	skipIfWindows(t)
	r, err := Command("sh", "-c", "echo $LAGODEV_TEST_VAR").
		Env("LAGODEV_TEST_VAR", "from-process").
		Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(r.Stdout) != "from-process" {
		t.Fatalf("env not applied: stdout=%q", r.Stdout)
	}
}

func TestClone_Immutability(t *testing.T) {
	base := Command("echo", "a")
	derived := base.Args("b", "c").Dir("/tmp").Env("X", "1")
	if len(base.args) != 1 || base.args[0] != "a" {
		t.Fatalf("base.args mutated: %v", base.args)
	}
	if base.dir != "" {
		t.Fatal("base.dir mutated")
	}
	if _, ok := base.env["X"]; ok {
		t.Fatal("base.env mutated")
	}
	if len(derived.args) != 2 || derived.args[0] != "b" {
		t.Fatalf("derived.args = %v", derived.args)
	}
}

func TestRun_UnknownBinary(t *testing.T) {
	_, err := Command("definitely-does-not-exist-xyz-12345").Run(context.Background())
	if err == nil {
		t.Fatal("must error on missing binary")
	}
	if errors.Is(err, ErrTimeout) {
		t.Fatal("must not be timeout error")
	}
}
