// Package jobs 提供后台任务与实时进度事件（SSE）分发。
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Event 是推送给前端的一条进度事件。
type Event struct {
	Job        string `json:"job"`
	Kind       string `json:"kind"` // start | progress | log | done | error | canceled
	Phase      string `json:"phase"`
	Current    string `json:"current"`
	Done       int64  `json:"done"`
	Total      int64  `json:"total"`
	Bytes      int64  `json:"bytes"`
	TotalBytes int64  `json:"totalBytes"`
	Message    string `json:"message"`
	Elapsed    int64  `json:"elapsed"`
}

// Hub 管理订阅者。
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[int]chan Event
	next int
}

func NewHub() *Hub {
	return &Hub{subs: map[string]map[int]chan Event{}}
}

func (h *Hub) Subscribe(job string) (<-chan Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[job] == nil {
		h.subs[job] = map[int]chan Event{}
	}
	id := h.next
	h.next++
	ch := make(chan Event, 256)
	h.subs[job][id] = ch
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if m, ok := h.subs[job]; ok {
			if c, ok := m[id]; ok {
				close(c)
				delete(m, id)
			}
			if len(m) == 0 {
				delete(h.subs, job)
			}
		}
	}
	return ch, cancel
}

func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs[e.Job] {
		select {
		case ch <- e:
		default: // 订阅端消费不过来时丢弃，保证清理流程不被阻塞
		}
	}
}

// Job 表示一个后台任务。
type Job struct {
	ID      string
	Kind    string
	Started time.Time

	mu     sync.Mutex
	cancel context.CancelFunc
	status string
	errMsg string
	result any
	last   Event

	hub  *Hub
	base context.Context
}

func (j *Job) Status() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.status
}

func (j *Job) Err() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.errMsg
}

// Progress 上报进度。
func (j *Job) Progress(e Event) {
	e.Job = j.ID
	e.Kind = "progress"
	e.Elapsed = time.Since(j.Started).Milliseconds()
	j.mu.Lock()
	j.last = e
	j.mu.Unlock()
	j.hub.Publish(e)
}

// Log 上报一条文本日志。
func (j *Job) Log(msg string) {
	j.hub.Publish(Event{Job: j.ID, Kind: "log", Message: msg,
		Elapsed: time.Since(j.Started).Milliseconds()})
}

// Done 标记任务成功结束。
func (j *Job) Done(result any, msg string) {
	j.mu.Lock()
	j.status = "done"
	j.result = result
	j.mu.Unlock()
	j.hub.Publish(Event{Job: j.ID, Kind: "done", Message: msg, Elapsed: time.Since(j.Started).Milliseconds()})
}

// Fail 标记任务失败。
func (j *Job) Fail(err error) {
	j.mu.Lock()
	j.status = "error"
	if err != nil {
		j.errMsg = err.Error()
	}
	j.mu.Unlock()
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	j.hub.Publish(Event{Job: j.ID, Kind: "error", Message: msg, Elapsed: time.Since(j.Started).Milliseconds()})
}

// Canceled 标记任务被用户取消。
func (j *Job) Canceled() {
	j.mu.Lock()
	j.status = "canceled"
	j.mu.Unlock()
	j.hub.Publish(Event{Job: j.ID, Kind: "canceled", Message: "任务已取消", Elapsed: time.Since(j.Started).Milliseconds()})
}

// Result 取回任务结果。
func (j *Job) Result() any {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.result
}

// Last 返回最后一条进度。
func (j *Job) Last() Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	e := j.last
	e.Job = j.ID
	return e
}

// Manager 管理任务生命周期。
type Manager struct {
	mu   sync.Mutex
	jobs map[string]*Job
	Hub  *Hub
}

func NewManager() *Manager {
	return &Manager{jobs: map[string]*Job{}, Hub: NewHub()}
}

// New 创建一个任务。
func (m *Manager) New(base context.Context, kind string) *Job {
	ctx, cancel := context.WithCancel(base)
	j := &Job{
		ID:      randID(),
		Kind:    kind,
		Started: time.Now(),
		status:  "running",
		hub:     m.Hub,
		base:    ctx,
		cancel:  cancel,
	}
	m.mu.Lock()
	m.jobs[j.ID] = j
	// 控制历史任务数量
	if len(m.jobs) > 64 {
		for k, v := range m.jobs {
			if k != j.ID && v.Status() != "running" {
				delete(m.jobs, k)
				if len(m.jobs) <= 48 {
					break
				}
			}
		}
	}
	m.mu.Unlock()
	m.Hub.Publish(Event{Job: j.ID, Kind: "start", Message: kind})
	return j
}

// Get 取出任务。
func (m *Manager) Get(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id]
}

// Cancel 取消任务。
func (m *Manager) Cancel(id string) bool {
	j := m.Get(id)
	if j == nil {
		return false
	}
	j.mu.Lock()
	c := j.cancel
	j.mu.Unlock()
	if c != nil {
		c()
		return true
	}
	return false
}

// Context 返回任务上下文。
func (j *Job) Context() context.Context { return j.base }

func randID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
