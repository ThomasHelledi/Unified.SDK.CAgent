package tmux

import (
	"sync"
	"time"
)

// EventType identifies the kind of tmux control-mode event.
type EventType int

const (
	// EventOutput is emitted when pane output is received (%output).
	EventOutput EventType = iota
	// EventSessionCreated is emitted when a new session is created.
	EventSessionCreated
	// EventSessionClosed is emitted when a session is destroyed.
	EventSessionClosed
	// EventPaneOutput is emitted for pane-specific output.
	EventPaneOutput
	// EventWindowChanged is emitted when the active window changes.
	EventWindowChanged
)

// String returns a human-readable name for the event type.
func (e EventType) String() string {
	switch e {
	case EventOutput:
		return "output"
	case EventSessionCreated:
		return "session-created"
	case EventSessionClosed:
		return "session-closed"
	case EventPaneOutput:
		return "pane-output"
	case EventWindowChanged:
		return "window-changed"
	default:
		return "unknown"
	}
}

// TmuxEvent represents a parsed event from tmux control mode.
type TmuxEvent struct {
	Type        EventType `json:"type"`
	SessionName string    `json:"sessionName,omitempty"`
	PaneID      string    `json:"paneId,omitempty"`
	Data        string    `json:"data,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
}

// eventBus is a simple pub/sub system for tmux events. Subscribers receive
// events on a buffered channel filtered by event type.
type eventBus struct {
	mu          sync.RWMutex
	subscribers map[EventType][]chan TmuxEvent
}

// newEventBus creates an event bus with no subscribers.
func newEventBus() *eventBus {
	return &eventBus{
		subscribers: make(map[EventType][]chan TmuxEvent),
	}
}

// Subscribe returns a channel that will receive events of the given type.
// The channel is buffered (64 events) to avoid blocking the publisher.
// Callers must drain the channel or events will be dropped.
func (b *eventBus) Subscribe(eventType EventType) <-chan TmuxEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan TmuxEvent, 64)
	b.subscribers[eventType] = append(b.subscribers[eventType], ch)
	return ch
}

// publish sends an event to all subscribers of that event type. Events are
// dropped (not blocking) if a subscriber channel is full.
func (b *eventBus) publish(event TmuxEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.subscribers[event.Type] {
		select {
		case ch <- event:
		default:
			// Subscriber channel full; drop event to avoid blocking.
		}
	}
}

// close closes all subscriber channels. After close, no new events should
// be published.
func (b *eventBus) close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	for eventType, channels := range b.subscribers {
		for _, ch := range channels {
			close(ch)
		}
		delete(b.subscribers, eventType)
	}
}
