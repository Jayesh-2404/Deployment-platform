package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Command is one external process invocation.
type Command struct {
	// Dir is the working directory. Empty means the current directory.
	Dir string
	// Binary is the executable name or absolute path.
	Binary string
	Args   []string
	// Env holds extra KEY=VALUE entries. They are appended to os.Environ()
	// rather than replacing it, so git and docker keep their own configuration
	// (credentials, SSH agent, Docker context).
	Env []string
}

// String renders the command for the deployment log. It never includes Env,
// because env values hold the project's secrets.
func (c Command) String() string {
	parts := make([]string, 0, len(c.Args)+1)
	parts = append(parts, c.Binary)
	for _, arg := range c.Args {
		if strings.ContainsAny(arg, " \t\"") {
			parts = append(parts, fmt.Sprintf("%q", arg))
			continue
		}
		parts = append(parts, arg)
	}
	return strings.Join(parts, " ")
}

// CommandRunner is the single boundary between the platform and the outside
// world. Every git and docker call goes through it, which is the reason the
// whole deployment pipeline is testable on a machine with no Docker installed.
type CommandRunner interface {
	// Output runs a command to completion and returns combined stdout+stderr.
	Output(ctx context.Context, cmd Command) (string, error)
	// Run runs a command and calls emit for every line of output as it
	// arrives, tagged "stdout" or "stderr". A nil emit discards output.
	Run(ctx context.Context, cmd Command, emit func(stream string, line string)) error
}

// ExecRunner is the real CommandRunner, backed by os/exec.
type ExecRunner struct{}

func NewExecRunner() *ExecRunner { return &ExecRunner{} }

func (r *ExecRunner) Output(ctx context.Context, cmd Command) (string, error) {
	var buffer bytes.Buffer
	err := r.Run(ctx, cmd, func(_ string, line string) { buffer.WriteString(line + "\n") })
	output := strings.TrimRight(buffer.String(), "\n")
	if err != nil {
		return output, err
	}
	return output, nil
}

func (r *ExecRunner) Run(ctx context.Context, cmd Command, emit func(stream string, line string)) error {
	process := exec.CommandContext(ctx, cmd.Binary, cmd.Args...)
	process.Dir = cmd.Dir
	if len(cmd.Env) > 0 {
		process.Env = append(os.Environ(), cmd.Env...)
	}

	stdout, err := process.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := process.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	if err := process.Start(); err != nil {
		return fmt.Errorf("start %s: %w", cmd.String(), err)
	}

	// Drain both pipes concurrently: a process that fills one pipe's OS buffer
	// while we block reading the other would deadlock.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.drain("stdout", stdout, emit) }()
	go func() { defer wg.Done(); r.drain("stderr", stderr, emit) }()

	waitErr := process.Wait()
	wg.Wait()

	if waitErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s: %w", cmd.String(), ctxErr)
		}
		return fmt.Errorf("%s: %w", cmd.String(), waitErr)
	}
	return nil
}

func (r *ExecRunner) drain(stream string, reader io.Reader, emit func(string, string)) {
	scanner := bufio.NewScanner(reader)
	// Docker build output can contain very long progress lines.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if emit == nil {
			continue
		}
		emit(stream, scanner.Text())
	}
}
