package tui

import (
	"sync"

	tea "charm.land/bubbletea/v2"
)

// queue is an unbounded FIFO of messages. The apply goroutine pushes progress
// events and must never block on a slow UI (a blocked engine would stall the
// deployment), so the buffer is unbounded; pop blocks until a message exists.
type queue struct {
	mu    sync.Mutex
	cond  *sync.Cond
	items []tea.Msg
}

func newQueue() *queue {
	q := &queue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *queue) push(m tea.Msg) {
	q.mu.Lock()
	q.items = append(q.items, m)
	q.mu.Unlock()
	q.cond.Signal()
}

func (q *queue) pop() tea.Msg {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 {
		q.cond.Wait()
	}
	m := q.items[0]
	q.items = q.items[1:]
	return m
}
