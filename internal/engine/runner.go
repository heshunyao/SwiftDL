package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"swiftdl/internal/model"
)

// 运行期错误，用于让管理器区分暂停/取消与真正的失败。
var (
	ErrPaused   = errors.New("已暂停")
	ErrCanceled = errors.New("已取消")
)

const readBufSize = 256 * 1024

type stopReason int

const (
	reasonNone stopReason = iota
	reasonPause
	reasonCancel
)

// Runner 负责单个任务的一次下载执行。暂停后重新 Run 即从分块断点继续。
type Runner struct {
	task   *model.Task
	Gen    int // 管理器用于识别过期的运行实例
	client *http.Client
	global *Throttle
	local  *Throttle

	saveFn func(model.TaskState)

	ctx      context.Context
	cancelFn context.CancelFunc

	mu       sync.Mutex
	reason   stopReason
	lastSave time.Time
}

// NewRunner 创建下载执行器。global 可为 nil；saveFn 用于周期性持久化进度。
func NewRunner(parent context.Context, task *model.Task, client *http.Client, global *Throttle, saveFn func(model.TaskState)) *Runner {
	ctx, cancel := context.WithCancel(parent)
	snap := task.View()
	return &Runner{
		task:     task,
		client:   client,
		global:   global,
		local:    NewThrottle(snap.SpeedLimit),
		saveFn:   saveFn,
		ctx:      ctx,
		cancelFn: cancel,
	}
}

// Pause 请求暂停（保留已下载进度）。
func (r *Runner) Pause() {
	r.setReason(reasonPause)
	r.cancelFn()
}

// Cancel 请求取消。
func (r *Runner) Cancel() {
	r.setReason(reasonCancel)
	r.cancelFn()
}

func (r *Runner) setReason(x stopReason) {
	r.mu.Lock()
	if r.reason == reasonNone {
		r.reason = x
	}
	r.mu.Unlock()
}

func (r *Runner) stopErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch r.reason {
	case reasonPause:
		return ErrPaused
	case reasonCancel:
		return ErrCanceled
	}
	return context.Canceled
}

// Run 执行下载：准备分块 → 并发拉取 → 校验 → 完成。
func (r *Runner) Run() error {
	snap := r.task.View()
	if err := os.MkdirAll(filepath.Dir(snap.SavePath), 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	f, err := os.OpenFile(snap.SavePath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("打开目标文件失败: %w", err)
	}
	defer f.Close()

	if snap.Size <= 0 || len(snap.Chunks) == 0 {
		if err := r.prepare(); err != nil {
			return err
		}
		snap = r.task.View()
	}

	// 预分配空间，避免边下边扩容产生磁盘碎片
	if err := f.Truncate(snap.Size); err != nil {
		return fmt.Errorf("预分配文件空间失败: %w", err)
	}

	if err := r.runWorkers(f, snap); err != nil {
		return err
	}

	if snap.ExpectedHash != "" {
		actual, verr := VerifyFile(snap.SavePath, snap.ExpectedHash)
		r.task.Update(func(s *model.TaskState) { s.ActualHash = actual })
		if verr != nil {
			return verr
		}
	}
	return nil
}

// prepare 重新探测并生成分块表（用于首次运行或元数据缺失的恢复任务）。
func (r *Runner) prepare() error {
	snap := r.task.View()
	meta, err := Probe(r.ctx, r.client, snap.URL)
	if err != nil {
		return fmt.Errorf("获取文件信息失败: %w", err)
	}
	if meta.Size <= 0 {
		return errors.New("服务器未返回文件大小，无法分块下载")
	}

	threads := snap.Threads
	if threads < 1 {
		threads = 8
	}
	r.task.Update(func(s *model.TaskState) {
		s.Size = meta.Size
		s.RangeSupported = meta.RangeSupported
		if s.ETag == "" {
			s.ETag = meta.ETag
		}
		if s.LastModified == "" {
			s.LastModified = meta.LastModified
		}
		if !meta.RangeSupported {
			// 不支持 Range 时退化为单连接顺序下载
			s.Threads = 1
			s.Chunks = []model.Chunk{{Index: 0, Start: 0, End: meta.Size - 1}}
			return
		}
		s.Threads = threads
		s.Chunks = SplitChunks(meta.Size, threads)
	})
	return nil
}

// runWorkers 启动固定数量的协程消费分块队列。
func (r *Runner) runWorkers(f *os.File, snap model.TaskState) error {
	threads := snap.Threads
	if threads < 1 {
		threads = 1
	}
	if threads > len(snap.Chunks) {
		threads = len(snap.Chunks)
	}

	indexes := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
			// 让其余分块尽快停下，避免失败时继续空跑
			r.cancelFn()
		}
		mu.Unlock()
	}

	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range indexes {
				if r.ctx.Err() != nil {
					return
				}
				if err := r.downloadChunk(f, idx); err != nil {
					if r.ctx.Err() != nil && errors.Is(err, context.Canceled) {
						return
					}
					fail(err)
					return
				}
			}
		}()
	}

	// 生产者：把未完成的分块派发给协程
	go func() {
		defer close(indexes)
		for _, c := range snap.Chunks {
			if c.Finished() {
				continue
			}
			select {
			case indexes <- c.Index:
			case <-r.ctx.Done():
				return
			}
		}
	}()

	wg.Wait()

	mu.Lock()
	err := firstErr
	mu.Unlock()
	if err != nil {
		return err
	}
	if r.ctx.Err() != nil {
		return r.stopErr()
	}
	return nil
}

// downloadChunk 带重试地下载一个分块。
func (r *Runner) downloadChunk(f *os.File, idx int) error {
	snap := r.task.View()
	attempts := snap.Retries
	if attempts < 0 {
		attempts = 0
	}
	delay := time.Duration(snap.RetryDelayMS) * time.Millisecond
	if delay <= 0 {
		delay = time.Second
	}

	var lastErr error
	for attempt := 0; attempt <= attempts; attempt++ {
		if attempt > 0 {
			if !r.sleep(delay) {
				return r.stopErr()
			}
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
		err := r.fetchChunk(f, idx)
		if err == nil {
			return nil
		}
		if r.ctx.Err() != nil {
			return r.stopErr()
		}
		lastErr = err
	}
	return fmt.Errorf("分块 #%d 重试 %d 次仍失败: %w", idx, attempts, lastErr)
}

// fetchChunk 拉取分块剩余部分，使用 WriteAt 直接写入目标偏移，天然支持断点续传。
func (r *Runner) fetchChunk(f *os.File, idx int) error {
	chunk := r.task.ChunkAt(idx)
	length := chunk.Length()
	if length <= 0 {
		return nil
	}
	done := chunk.Done
	if done > length {
		done = length
	}
	remaining := length - done
	if remaining == 0 {
		return nil
	}

	snap := r.task.View()
	urls := r.task.URLs()
	if len(urls) == 0 {
		return errors.New("下载地址为空")
	}
	// 多源下载：不同分块轮流走不同镜像
	target := urls[idx%len(urls)]

	offset := chunk.Start + done
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept-Encoding", "identity")
	if snap.RangeSupported {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, chunk.End))
	} else {
		// 不支持 Range：从头顺序下载，已写入部分直接跳过
		offset = chunk.Start + done
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if snap.RangeSupported {
		switch resp.StatusCode {
		case http.StatusPartialContent:
			if start, ok := parseRangeStart(resp.Header.Get("Content-Range")); ok && start != offset {
				return fmt.Errorf("服务器返回的区间起点不符（期望 %d，实际 %d）", offset, start)
			}
		case http.StatusOK:
			return errors.New("服务器忽略了 Range 请求，已退化为单连接")
		case http.StatusRequestedRangeNotSatisfiable:
			return fmt.Errorf("服务器拒绝区间 %d-%d（HTTP 416）", offset, chunk.End)
		default:
			return fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
		}
	} else {
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			return fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
		}
		if done > 0 {
			if _, err := io.CopyN(io.Discard, resp.Body, done); err != nil {
				return err
			}
		}
	}

	buf := make([]byte, readBufSize)
	pos := offset
	written := int64(0)
	for written < remaining {
		if r.ctx.Err() != nil {
			return r.stopErr()
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.WriteAt(buf[:n], pos); werr != nil {
				return fmt.Errorf("写盘失败: %w", werr)
			}
			pos += int64(n)
			written += int64(n)
			r.task.AdvanceChunk(idx, int64(n))
			if r.global != nil {
				r.global.Acquire(n)
			}
			r.local.Acquire(n)
			r.maybePersist()
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return rerr
		}
	}
	if written < remaining {
		return io.ErrUnexpectedEOF
	}
	return nil
}

// sleep 可被暂停/取消打断的等待。
func (r *Runner) sleep(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-r.ctx.Done():
		return false
	}
}

// maybePersist 限制进度落盘频率（约 2 秒一次），避免频繁写 JSON。
func (r *Runner) maybePersist() {
	if r.saveFn == nil {
		return
	}
	r.mu.Lock()
	if time.Since(r.lastSave) < 2*time.Second {
		r.mu.Unlock()
		return
	}
	r.lastSave = time.Now()
	r.mu.Unlock()
	r.saveFn(r.task.View())
}

// parseRangeStart 解析 "bytes 100-200/12345" 的起点。
func parseRangeStart(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(strings.ToLower(v), "bytes ") {
		return 0, false
	}
	v = strings.TrimPrefix(v, "bytes ")
	v = strings.TrimPrefix(v, "Bytes ")
	dash := strings.Index(v, "-")
	if dash <= 0 {
		return 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(v[:dash]), 10, 64)
	if err != nil {
		return 0, false
	}
	return start, true
}
