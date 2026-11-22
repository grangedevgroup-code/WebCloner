package cloner

import "sync"

type queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []job
	active int
	closed bool
}

func newQueue() *queue {
	q := &queue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *queue) push(j job) {
	q.mu.Lock()
	if !q.closed {
		q.items = append(q.items, j)
	}
	q.mu.Unlock()
	q.cond.Signal()
}

func (q *queue) pop() (job, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && q.active > 0 && !q.closed {
		q.cond.Wait()
	}
	if q.closed || len(q.items) == 0 {
		q.closed = true
		q.cond.Broadcast()
		return job{}, false
	}
	j := q.items[0]
	q.items[0] = job{}
	q.items = q.items[1:]
	q.active++
	return j, true
}

func (q *queue) done() {
	q.mu.Lock()
	q.active--
	q.mu.Unlock()
	q.cond.Broadcast()
}

func (q *queue) close() {
	q.mu.Lock()
	q.closed = true
	q.items = nil
	q.mu.Unlock()
	q.cond.Broadcast()
}
