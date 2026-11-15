package engine

import (
	"sync"
	"time"
)

// Throttle 是简单的令牌桶限速器：按累计字节数与理论用时的差值做阻塞。
type Throttle struct {
	mu    sync.Mutex
	rate  int64
	start time.Time
	sent  int64
}

// NewThrottle 创建限速器，rate 为字节/秒，<=0 表示不限速。
func NewThrottle(rate int64) *Throttle {
	return &Throttle{rate: rate}
}

// Acquire 在写入 n 字节后调用，若已超速则阻塞等待。
func (t *Throttle) Acquire(n int) {
	if n <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.rate <= 0 {
		return
	}
	if t.start.IsZero() {
		t.start = time.Now()
	}
	t.sent += int64(n)
	due := time.Duration(float64(t.sent) / float64(t.rate) * float64(time.Second))
	if wait := due - time.Since(t.start); wait > 0 {
		time.Sleep(wait)
	}
}

// SetRate 动态调整速率，同时重置计时窗口。
func (t *Throttle) SetRate(rate int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rate = rate
	t.start = time.Time{}
	t.sent = 0
}

// Rate 返回当前限速值。
func (t *Throttle) Rate() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rate
}
