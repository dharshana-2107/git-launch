package deployment

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Runner runs external programs (git, docker).
//
// WHY an interface: the worker's logic (statuses, logs, error handling) is
// tested with a fake Runner, so tests need neither git nor Docker.
type Runner interface {
	// Run runs the command in dir and calls onLine for every line it prints
	// (stdout and stderr together, in order).
	Run(ctx context.Context, dir string, onLine func(string), name string, args ...string) error
	// Output runs the command and returns what it printed, trimmed.
	Output(ctx context.Context, dir string, name string, args ...string) (string, error)
}

// ExecRunner is the real Runner, built on os/exec.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir string, onLine func(string), name string, args ...string) error {
	// CommandContext kills the process when ctx is cancelled (timeout or shutdown).
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = commandEnv()

	// One pipe for both outputs, so lines arrive in the order they were printed.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}

	// Wait in a goroutine; closing the writer ends the reading loop below.
	waitErr := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pw.Close()
		waitErr <- err
	}()

	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024) // allow lines up to 1 MB
	for sc.Scan() {
		onLine(sc.Text())
	}
	// Drain anything left so the process never blocks writing to a full pipe.
	_, _ = io.Copy(io.Discard, pr)

	if err := <-waitErr; err != nil {
		if ctx.Err() != nil {
			return ctx.Err() // report "timed out"/"cancelled", not "signal: killed"
		}
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func (ExecRunner) Output(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = commandEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// commandEnv is the server's environment plus GIT_TERMINAL_PROMPT=0.
// Without it, cloning a private or missing repo makes git wait forever for a
// username that nobody will type, and the build hangs.
func commandEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
}
