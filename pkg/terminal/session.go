package terminal

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// SessionState represents the lifecycle state of a terminal session.
type SessionState string

const (
	StateStarting  SessionState = "starting"
	StateRunning   SessionState = "running"
	StateCompleted SessionState = "completed"
	StateFailed    SessionState = "failed"
	StateStopped   SessionState = "stopped"
)

// OutputLine is a single line captured from a session's stdout/stderr.
type OutputLine struct {
	Content   string    `json:"content"`
	Stream    string    `json:"stream"` // "stdout" or "stderr"
	Timestamp time.Time `json:"timestamp"`
}

// Session is a managed child process with piped I/O and output buffering.
// Uses a long-running `cat | sh` pipeline to keep the session alive
// while accepting commands on stdin.
type Session struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	State     SessionState `json:"state"`
	ExitCode  int          `json:"exit_code"`
	CreatedAt time.Time    `json:"created_at"`
	ExitedAt  *time.Time   `json:"exited_at,omitempty"`
	Shell     string       `json:"shell"`

	mu       sync.RWMutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	cancel   context.CancelFunc
	output   []OutputLine
	maxLines int
	onChange func(s *Session, line OutputLine)
}

// NewSession creates a session that will use the given shell.
func NewSession(id, name, shell string, maxLines int) *Session {
	if shell == "" {
		shell = "/bin/sh"
	}
	if maxLines <= 0 {
		maxLines = 500
	}
	return &Session{
		ID:        id,
		Name:      name,
		State:     StateStarting,
		CreatedAt: time.Now().UTC(),
		Shell:     shell,
		output:    make([]OutputLine, 0, maxLines),
		maxLines:  maxLines,
	}
}

// OnOutput sets a callback invoked for every new output line.
func (s *Session) OnOutput(fn func(*Session, OutputLine)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onChange = fn
}

// Start launches a persistent shell process that reads commands from stdin.
// Uses `tail -f /dev/null | <shell>` trick to keep stdin open, then writes
// commands to the shell's stdin pipe.
func (s *Session) Start(ctx context.Context, workDir string, env []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	childCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// The key trick: use the shell directly. The StdinPipe stays open
	// as long as we don't close it, keeping the shell alive.
	s.cmd = exec.CommandContext(childCtx, s.Shell)
	if workDir != "" {
		s.cmd.Dir = workDir
	}
	if len(env) > 0 {
		s.cmd.Env = append(s.cmd.Environ(), env...)
	}

	stdin, err := s.cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdin pipe: %w", err)
	}
	s.stdin = stdin

	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := s.cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := s.cmd.Start(); err != nil {
		cancel()
		s.State = StateFailed
		return fmt.Errorf("start: %w", err)
	}

	s.State = StateRunning

	go s.readStream(stdout, "stdout")
	go s.readStream(stderr, "stderr")
	go s.waitForExit()

	return nil
}

// SendInput writes a command to the shell's stdin.
func (s *Session) SendInput(input string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.State != StateRunning {
		return fmt.Errorf("session %s is not running (state: %s)", s.ID, s.State)
	}
	if s.stdin == nil {
		return fmt.Errorf("session %s has no stdin", s.ID)
	}

	if len(input) == 0 || input[len(input)-1] != '\n' {
		input += "\n"
	}

	_, err := io.WriteString(s.stdin, input)
	return err
}

// Stop kills the process.
func (s *Session) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.State != StateRunning && s.State != StateStarting {
		return nil
	}

	// Close stdin first — this signals the shell to exit gracefully
	if s.stdin != nil {
		_ = s.stdin.Close()
	}

	if s.cancel != nil {
		s.cancel()
	}

	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}

	s.State = StateStopped
	now := time.Now().UTC()
	s.ExitedAt = &now
	return nil
}

// Output returns the last N lines from the output buffer.
func (s *Session) Output(lastN int) []OutputLine {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if lastN <= 0 || lastN > len(s.output) {
		lastN = len(s.output)
	}
	start := len(s.output) - lastN
	result := make([]OutputLine, lastN)
	copy(result, s.output[start:])
	return result
}

// OutputCount returns total lines captured.
func (s *Session) OutputCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.output)
}

func (s *Session) readStream(r io.Reader, stream string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := OutputLine{
			Content:   scanner.Text(),
			Stream:    stream,
			Timestamp: time.Now().UTC(),
		}

		s.mu.Lock()
		if len(s.output) >= s.maxLines {
			s.output = s.output[1:]
		}
		s.output = append(s.output, line)
		cb := s.onChange
		s.mu.Unlock()

		if cb != nil {
			cb(s, line)
		}
	}
}

func (s *Session) waitForExit() {
	if s.cmd == nil {
		return
	}

	err := s.cmd.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	s.ExitedAt = &now

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			s.ExitCode = exitErr.ExitCode()
		} else {
			s.ExitCode = -1
		}
		if s.State == StateRunning {
			s.State = StateFailed
		}
	} else {
		s.ExitCode = 0
		if s.State == StateRunning {
			s.State = StateCompleted
		}
	}
}
