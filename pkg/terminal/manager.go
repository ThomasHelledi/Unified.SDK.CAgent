package terminal

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Manager manages multiple concurrent terminal sessions.
// Thread-safe, no external dependencies (no tmux).
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	counter  atomic.Int64
	maxLines int
	shell    string
}

// NewManager creates a session manager.
func NewManager(shell string, bufferLines int) *Manager {
	if shell == "" {
		shell = "/bin/zsh"
	}
	if bufferLines <= 0 {
		bufferLines = 500
	}
	return &Manager{
		sessions: make(map[string]*Session),
		maxLines: bufferLines,
		shell:    shell,
	}
}

// CreateSession creates and starts a new terminal session.
func (m *Manager) CreateSession(ctx context.Context, name, workDir string, env []string) (*Session, error) {
	id := fmt.Sprintf("th-%d", m.counter.Add(1))

	session := NewSession(id, name, m.shell, m.maxLines)

	// Log output in real-time
	session.OnOutput(func(s *Session, line OutputLine) {
		slog.Debug("session output",
			"session", s.ID,
			"stream", line.Stream,
			"line", truncate(line.Content, 120),
		)
	})

	// Use background context — sessions outlive HTTP requests.
	// The request ctx is only for the API call, not the session lifecycle.
	if err := session.Start(context.Background(), workDir, env); err != nil {
		return nil, fmt.Errorf("start session %s: %w", id, err)
	}

	m.mu.Lock()
	m.sessions[id] = session
	m.mu.Unlock()

	slog.Info("session created", "id", id, "name", name, "workDir", workDir)
	return session, nil
}

// GetSession returns a session by ID.
func (m *Manager) GetSession(id string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// ListSessions returns all sessions.
func (m *Manager) ListSessions() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		result = append(result, s)
	}
	return result
}

// StopSession stops a session by ID.
func (m *Manager) StopSession(id string) error {
	m.mu.RLock()
	session, ok := m.sessions[id]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session %s not found", id)
	}

	if err := session.Stop(); err != nil {
		return fmt.Errorf("stop session %s: %w", id, err)
	}

	slog.Info("session stopped", "id", id)
	return nil
}

// DeleteSession stops and removes a session.
func (m *Manager) DeleteSession(id string) error {
	m.mu.Lock()
	session, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session %s not found", id)
	}
	delete(m.sessions, id)
	m.mu.Unlock()

	_ = session.Stop()
	slog.Info("session deleted", "id", id)
	return nil
}

// SendInput sends input to a session.
func (m *Manager) SendInput(id, input string) error {
	session := m.GetSession(id)
	if session == nil {
		return fmt.Errorf("session %s not found", id)
	}
	return session.SendInput(input)
}

// RunCommand creates a session, runs a command, waits for exit, and returns output.
// Useful for one-shot commands.
func (m *Manager) RunCommand(ctx context.Context, name, command, workDir string) (*Session, error) {
	session, err := m.CreateSession(ctx, name, workDir, nil)
	if err != nil {
		return nil, err
	}

	if err := session.SendInput(command); err != nil {
		return session, fmt.Errorf("send command: %w", err)
	}

	// Wait for command to finish (with timeout from context)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = session.Stop()
			return session, ctx.Err()
		case <-ticker.C:
			session.mu.RLock()
			state := session.State
			session.mu.RUnlock()
			if state != StateRunning && state != StateStarting {
				return session, nil
			}
		}
	}
}

// ActiveCount returns the number of running sessions.
func (m *Manager) ActiveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, s := range m.sessions {
		s.mu.RLock()
		if s.State == StateRunning || s.State == StateStarting {
			count++
		}
		s.mu.RUnlock()
	}
	return count
}

// StopAll stops all running sessions.
func (m *Manager) StopAll() {
	m.mu.RLock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.RUnlock()

	for _, s := range sessions {
		_ = s.Stop()
	}
	slog.Info("all sessions stopped", "count", len(sessions))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
