// Package fakebe provides an in-memory backend for tests.
package fakebe

import (
	"context"
	"fmt"
	"sync"

	"pfqueue/internal/backend"
)

// Msg is a fake queued message.
type Msg struct {
	ID, From, To, Subject, Body string
}

// Backend keeps its queues in memory and records actions.
type Backend struct {
	mu      sync.Mutex
	Queues  [][]Msg
	cur     int
	Actions []string // "<action> <id>"
}

// New returns a backend with the given queues.
func New(queues ...[]Msg) *Backend { return &Backend{Queues: queues} }

func (b *Backend) ID() string                                 { return "fake" }
func (b *Backend) Version() string                            { return "0" }
func (b *Backend) Setup(backend.Config) error                 { return nil }
func (b *Backend) Close() error                               { return nil }
func (b *Backend) QueueCount() int                            { return len(b.Queues) }
func (b *Backend) QueueName(q int) string                     { return fmt.Sprintf("q%d", q) }
func (b *Backend) Caps() backend.Caps                         { return backend.CapDelete | backend.CapEnvelope }
func (b *Backend) Status(backend.Entry) (string, bool, error) { return "Fake", true, nil }

func (b *Backend) SetQueue(q int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cur = q
	return nil
}

// Set replaces the content of queue q.
func (b *Backend) Set(q int, msgs []Msg) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Queues[q] = msgs
}

func (b *Backend) find(id string) (Msg, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, m := range b.Queues[b.cur] {
		if m.ID == id {
			return m, true
		}
	}
	return Msg{}, false
}

func (b *Backend) Scan(_ context.Context, max int) ([]backend.Entry, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []backend.Entry
	for _, m := range b.Queues[b.cur] {
		if len(out) >= max {
			break
		}
		out = append(out, backend.Entry{ID: m.ID, Path: "/q/" + m.ID})
	}
	return out, nil
}

func (b *Backend) Headers(e backend.Entry, envelope bool) (backend.Headers, error) {
	m, ok := b.find(e.ID)
	if !ok {
		return backend.Headers{}, fmt.Errorf("no message %s", e.ID)
	}
	h := backend.Headers{From: m.From, To: m.To, Subject: m.Subject, Complete: true}
	if envelope {
		h.From = "env-" + h.From
	}
	return h, nil
}

func (b *Backend) Body(e backend.Entry, max int) ([]byte, error) {
	m, ok := b.find(e.ID)
	if !ok {
		return nil, fmt.Errorf("no message %s", e.ID)
	}
	body := []byte(m.Body)
	if len(body) > max {
		body = body[:max]
	}
	return body, nil
}

func (b *Backend) Action(id string, a backend.Action) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Actions = append(b.Actions, a.String()+" "+id)
	if a == backend.ActionDelete {
		q := b.Queues[b.cur][:0]
		for _, m := range b.Queues[b.cur] {
			if m.ID != id {
				q = append(q, m)
			}
		}
		b.Queues[b.cur] = q
	}
	return nil
}
