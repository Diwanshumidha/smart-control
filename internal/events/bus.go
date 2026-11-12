// Package events is an in-process pub/sub bus. The desktop app and the admin
// SSE stream subscribe to it to show what the AI is doing live.
package events

import (
	"sync"
	"time"
)

// Well-known topics. Toolsets may publish their own (e.g. "brag.run").
const (
	ToolStarted     = "tool.started"
	ToolFinished    = "tool.finished"
	PostChanged     = "post.changed"
	ResourceAdded   = "resource.added"
	ApprovalRequest = "url.approval_requested"
	ApprovalDecided = "url.approval_decided"
	ServerStatus    = "server.status"
	LogLine         = "log"
)

type Event struct {
	Topic string    `json:"topic"`
	At    time.Time `json:"at"`
	Data  any       `json:"data"`
}

type Bus struct {
	mu   sync.RWMutex
	seq  int
	subs map[int]chan Event
}

func NewBus() *Bus { return &Bus{subs: map[int]chan Event{}} }

// Publish never blocks: a slow subscriber drops events rather than stalling a tool call.
func (b *Bus) Publish(topic string, data any) {
	ev := Event{Topic: topic, At: time.Now(), Data: data}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Subscribe returns a channel of events and a cancel func that closes it.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	b.mu.Lock()
	b.seq++
	id := b.seq
	b.subs[id] = ch
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			b.mu.Unlock()
			close(ch)
		})
	}
}
