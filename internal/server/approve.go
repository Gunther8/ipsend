package server

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Decision 是用户对一次接收请求的决定。
type Decision int

const (
	Reject Decision = iota
	Accept
	RejectAndBlock
)

// Request 是展示给用户确认的接收请求。
type Request struct {
	Kind         string // "offer" 或 "curl"
	RemoteIP     string
	SenderName   string
	FileCount    int
	TotalSize    int64
	Names        []string // 前若干个文件名，用于展示
	Sizes        []int64  // 与 Names 对应的大小（目录为 -1）
	Resume       bool
	ResumedBytes int64
	Deadline     time.Time // 超过此时间自动拒绝
}

// Approver 由界面层实现：同一时间只会被调用一次（服务端负责排队），
// 实现方应在 ctx 结束或 Deadline 到达时返回 Reject。
type Approver interface {
	Approve(ctx context.Context, req Request) Decision
}

// AutoApprover 自动接受（测试用）。
type AutoApprover struct{}

func (AutoApprover) Approve(context.Context, Request) Decision { return Accept }

const (
	ApproveTimeout = 60 * time.Second
	maxQueue       = 10
	maxQueuePerIP  = 3
)

var (
	errQueueFull   = errors.New("queue full")
	errIPQueueFull = errors.New("too many pending requests from this ip")
)

// approvalQueue 让请求排队、一次只交给 Approver 一个，并限制队列长度。
type approvalQueue struct {
	approver Approver
	turn     chan struct{} // 容量 1 的令牌
	mu       sync.Mutex
	total    int
	perIP    map[string]int
	blocked  map[string]bool
	onChange func([]string) // 屏蔽列表变化时回调（用于持久化），可为空
}

func newApprovalQueue(a Approver, blocked []string, onChange func([]string)) *approvalQueue {
	q := &approvalQueue{
		approver: a,
		turn:     make(chan struct{}, 1),
		perIP:    map[string]int{},
		blocked:  map[string]bool{},
		onChange: onChange,
	}
	for _, ip := range blocked {
		q.blocked[ip] = true
	}
	q.turn <- struct{}{}
	return q
}

func (q *approvalQueue) IsBlocked(ip string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.blocked[ip]
}

func (q *approvalQueue) list() []string {
	out := make([]string, 0, len(q.blocked))
	for ip := range q.blocked {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

// Blocked 返回已屏蔽的 IP（排序后）。
func (q *approvalQueue) Blocked() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.list()
}

func (q *approvalQueue) setBlocked(ip string, on bool) {
	q.mu.Lock()
	if q.blocked[ip] == on {
		q.mu.Unlock()
		return
	}
	if on {
		q.blocked[ip] = true
	} else {
		delete(q.blocked, ip)
	}
	l := q.list()
	cb := q.onChange
	q.mu.Unlock()
	if cb != nil {
		cb(l)
	}
}

func (q *approvalQueue) Block(ip string)   { q.setBlocked(ip, true) }
func (q *approvalQueue) Unblock(ip string) { q.setBlocked(ip, false) }

// Ask 排队等待用户决定。ctx 取消（对方断开）时立即放弃排队并返回 Reject。
func (q *approvalQueue) Ask(ctx context.Context, req Request) (Decision, error) {
	q.mu.Lock()
	if q.total >= maxQueue {
		q.mu.Unlock()
		return Reject, errQueueFull
	}
	if q.perIP[req.RemoteIP] >= maxQueuePerIP {
		q.mu.Unlock()
		return Reject, errIPQueueFull
	}
	q.total++
	q.perIP[req.RemoteIP]++
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.total--
		if q.perIP[req.RemoteIP]--; q.perIP[req.RemoteIP] <= 0 {
			delete(q.perIP, req.RemoteIP)
		}
		q.mu.Unlock()
	}()

	select {
	case <-q.turn:
	case <-ctx.Done():
		return Reject, nil
	}
	defer func() { q.turn <- struct{}{} }()

	if q.IsBlocked(req.RemoteIP) { // 排队期间被屏蔽
		return Reject, nil
	}
	req.Deadline = time.Now().Add(ApproveTimeout)
	actx, cancel := context.WithDeadline(ctx, req.Deadline)
	defer cancel()
	d := q.approver.Approve(actx, req)
	if actx.Err() != nil && d == Accept { // 超时或对方断开后才点的接受，按拒绝处理
		d = Reject
	}
	if d == RejectAndBlock {
		q.Block(req.RemoteIP)
	}
	return d, nil
}
