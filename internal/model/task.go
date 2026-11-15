package model

import (
	"sync"
	"time"
)

// Status 任务状态。
type Status string

const (
	StatusQueued      Status = "queued"
	StatusDownloading Status = "downloading"
	StatusPaused      Status = "paused"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusCanceled    Status = "canceled"
)

// Text 返回用于界面展示的中文状态名。
func (s Status) Text() string {
	switch s {
	case StatusQueued:
		return "等待中"
	case StatusDownloading:
		return "下载中"
	case StatusPaused:
		return "已暂停"
	case StatusCompleted:
		return "已完成"
	case StatusFailed:
		return "失败"
	case StatusCanceled:
		return "已取消"
	}
	return string(s)
}

// Chunk 是一个分块（左闭右闭区间）。
type Chunk struct {
	Index int   `json:"index"`
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	// Done 表示该分块已落盘的字节数（相对 Start）。
	Done int64 `json:"done"`
}

// Length 分块总长度。
func (c Chunk) Length() int64 { return c.End - c.Start + 1 }

// Finished 分块是否已完成。
func (c Chunk) Finished() bool { return c.Done >= c.Length() }

// TaskState 是可序列化的任务数据。
type TaskState struct {
	ID             string     `json:"id"`
	URL            string     `json:"url"`
	Mirrors        []string   `json:"mirrors,omitempty"`
	SavePath       string     `json:"savePath"`
	Size           int64      `json:"size"`
	RangeSupported bool       `json:"rangeSupported"`
	ETag           string     `json:"etag,omitempty"`
	LastModified   string     `json:"lastModified,omitempty"`
	Threads        int        `json:"threads"`
	Retries        int        `json:"retries"`
	RetryDelayMS   int        `json:"retryDelayMs"`
	SpeedLimit     int64      `json:"speedLimit"`
	Status         Status     `json:"status"`
	Chunks         []Chunk    `json:"chunks"`
	Downloaded     int64      `json:"downloaded"`
	Error          string     `json:"error,omitempty"`
	ExpectedHash   string     `json:"expectedHash,omitempty"`
	ActualHash     string     `json:"actualHash,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
}

// Task 在 TaskState 外加读写锁，下载协程与 HTTP 接口共享同一实例。
type Task struct {
	TaskState
	mu sync.RWMutex
}

// NewTask 由持久化数据构造任务。
func NewTask(state TaskState) *Task { return &Task{TaskState: state} }

// View 返回加锁后的状态深拷贝，供界面与持久化使用。
func (t *Task) View() TaskState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s := t.TaskState
	if t.Chunks != nil {
		s.Chunks = append([]Chunk(nil), t.Chunks...)
	}
	if t.Mirrors != nil {
		s.Mirrors = append([]string(nil), t.Mirrors...)
	}
	return s
}

// Update 在写锁内修改任务状态。
func (t *Task) Update(fn func(*TaskState)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fn(&t.TaskState)
	t.UpdatedAt = time.Now()
}

// ChunkAt 读取单个分块快照。
func (t *Task) ChunkAt(i int) Chunk {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if i < 0 || i >= len(t.Chunks) {
		return Chunk{}
	}
	return t.Chunks[i]
}

// AdvanceChunk 累加分块进度与总进度。
func (t *Task) AdvanceChunk(i int, n int64) {
	if n <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if i < 0 || i >= len(t.Chunks) {
		return
	}
	t.Chunks[i].Done += n
	t.Downloaded += n
	t.UpdatedAt = time.Now()
}

// URLs 返回主地址 + 镜像地址，用于多源下载。
func (t *Task) URLs() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	urls := make([]string, 0, len(t.Mirrors)+1)
	urls = append(urls, t.URL)
	urls = append(urls, t.Mirrors...)
	return urls
}
