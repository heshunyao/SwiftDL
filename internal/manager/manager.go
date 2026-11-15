package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"swiftdl/internal/config"
	"swiftdl/internal/engine"
	"swiftdl/internal/model"
	"swiftdl/internal/store"
)

// ErrTaskNotFound 任务不存在。
var ErrTaskNotFound = errors.New("任务不存在")

// probeTimeout 解析远端文件信息的超时时间。
const probeTimeout = 45 * time.Second

// AddRequest 新建任务请求。
type AddRequest struct {
	URL          string   `json:"url"`
	Mirrors      []string `json:"mirrors"`
	Dir          string   `json:"dir"`
	Filename     string   `json:"filename"`
	Threads      int      `json:"threads"`
	SpeedLimit   int64    `json:"speedLimit"`
	Retries      int      `json:"retries"`
	ExpectedHash string   `json:"expectedHash"`
}

// TaskDTO 是返回给前端的任务视图。
type TaskDTO struct {
	model.TaskState
	Speed      int64   `json:"speed"`
	ETASeconds int64   `json:"etaSeconds"`
	Progress   float64 `json:"progress"`
	StatusText string  `json:"statusText"`
}

// GlobalStats 全局统计。
type GlobalStats struct {
	DownloadSpeed int64 `json:"downloadSpeed"`
	ActiveTasks   int   `json:"activeTasks"`
	TotalTasks    int   `json:"totalTasks"`
}

// State 前端轮询的完整状态。
type State struct {
	Tasks  []TaskDTO     `json:"tasks"`
	Global GlobalStats   `json:"global"`
	Config config.Config `json:"config"`
}

type speedSample struct {
	bytes int64
	at    time.Time
	speed int64
}

// Manager 管理任务队列、并发调度与进度持久化。
type Manager struct {
	mu      sync.Mutex
	cfg     config.Config
	cfgPath string
	store   *store.Store
	tasks   []*model.Task
	runners map[string]*engine.Runner
	gen     map[string]int
	active  int
	samples map[string]speedSample
	pending map[string]string // 等待协程退出后再删除的本地文件
	client  *http.Client
	global  *engine.Throttle
}

// New 从磁盘恢复任务并启动调度。
func New(cfg config.Config, cfgPath string, st *store.Store) (*Manager, error) {
	states, err := st.Load()
	if err != nil {
		return nil, err
	}

	m := &Manager{
		cfg:     cfg,
		cfgPath: cfgPath,
		store:   st,
		runners: map[string]*engine.Runner{},
		gen:     map[string]int{},
		samples: map[string]speedSample{},
		pending: map[string]string{},
		client:  engine.NewClient(cfg.Proxy),
		global:  engine.NewThrottle(cfg.GlobalSpeedLimit),
	}

	for _, s := range states {
		// 上次进程退出时仍在下载的任务重新排队，实现重启续传
		if s.Status == model.StatusDownloading || s.Status == model.StatusQueued {
			s.Status = model.StatusQueued
		}
		s.Error = ""
		m.tasks = append(m.tasks, model.NewTask(s))
	}

	go m.samplingLoop()

	m.mu.Lock()
	m.scheduleLocked()
	m.mu.Unlock()
	return m, nil
}

// Add 探测远端文件信息、创建任务并入队。
func (m *Manager) Add(req AddRequest) (TaskDTO, error) {
	rawURL := strings.TrimSpace(req.URL)
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return TaskDTO{}, errors.New("请填写有效的 http/https 下载地址")
	}

	mirrors := make([]string, 0, len(req.Mirrors))
	for _, item := range req.Mirrors {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		u, err := url.Parse(item)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return TaskDTO{}, fmt.Errorf("镜像地址无效: %s", item)
		}
		mirrors = append(mirrors, item)
	}

	m.mu.Lock()
	client := m.client
	cfg := m.cfg
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	meta, err := engine.Probe(ctx, client, rawURL)
	cancel()
	if err != nil {
		return TaskDTO{}, fmt.Errorf("解析下载地址失败: %w", err)
	}

	name := strings.TrimSpace(req.Filename)
	if name == "" {
		name = meta.Filename
	}
	if name == "" {
		name = "download.bin"
	}
	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		dir = cfg.SaveDir
	}
	savePath, err := uniquePath(dir, name)
	if err != nil {
		return TaskDTO{}, err
	}

	threads := req.Threads
	if threads <= 0 {
		threads = cfg.DefaultThreads
	}
	if threads < 1 {
		threads = 1
	}
	if threads > 128 {
		threads = 128
	}

	retries := req.Retries
	if retries < 0 {
		retries = cfg.Retries
	}
	delay := cfg.RetryDelayMS
	if delay <= 0 {
		delay = 1000
	}

	now := time.Now()
	st := model.TaskState{
		ID:             newID(),
		URL:            rawURL,
		Mirrors:        mirrors,
		SavePath:       savePath,
		Size:           meta.Size,
		RangeSupported: meta.RangeSupported,
		ETag:           meta.ETag,
		LastModified:   meta.LastModified,
		Threads:        threads,
		Retries:        retries,
		RetryDelayMS:   delay,
		SpeedLimit:     req.SpeedLimit,
		Status:         model.StatusQueued,
		ExpectedHash:   strings.TrimSpace(req.ExpectedHash),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if meta.Size > 0 {
		if meta.RangeSupported {
			st.Chunks = engine.SplitChunks(meta.Size, threads)
		} else {
			// 服务器不支持 Range，退化为单连接
			st.Threads = 1
			st.Chunks = []model.Chunk{{Index: 0, Start: 0, End: meta.Size - 1}}
		}
	}

	task := model.NewTask(st)
	m.mu.Lock()
	m.tasks = append(m.tasks, task)
	m.persistLocked()
	m.scheduleLocked()
	dto := m.toDTOLocked(task)
	m.mu.Unlock()
	return dto, nil
}

// Pause 暂停任务。
func (m *Manager) Pause(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	t := m.findLocked(id)
	if t == nil {
		return ErrTaskNotFound
	}
	if r := m.runners[id]; r != nil {
		r.Pause()
		m.gen[id]++ // 使这次运行失效，避免结束后覆盖暂停状态
		delete(m.runners, id)
	}
	t.Update(func(s *model.TaskState) {
		if s.Status == model.StatusDownloading || s.Status == model.StatusQueued {
			s.Status = model.StatusPaused
		}
	})
	m.persistLocked()
	return nil
}

// Resume 继续下载（对失败任务即重试）。
func (m *Manager) Resume(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	t := m.findLocked(id)
	if t == nil {
		return ErrTaskNotFound
	}
	t.Update(func(s *model.TaskState) {
		if s.Status == model.StatusCompleted {
			return
		}
		s.Status = model.StatusQueued
		s.Error = ""
	})
	m.persistLocked()
	m.scheduleLocked()
	return nil
}

// Cancel 取消任务，保留已下载的部分文件。
func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	t := m.findLocked(id)
	if t == nil {
		return ErrTaskNotFound
	}
	if r := m.runners[id]; r != nil {
		r.Cancel()
		m.gen[id]++
		delete(m.runners, id)
	}
	t.Update(func(s *model.TaskState) {
		s.Status = model.StatusCanceled
		s.Error = ""
	})
	m.persistLocked()
	return nil
}

// Remove 删除任务，deleteFile 为 true 时同时删除本地文件。
func (m *Manager) Remove(id string, deleteFile bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := -1
	for i, t := range m.tasks {
		if t.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrTaskNotFound
	}
	task := m.tasks[idx]
	view := task.View()

	active := false
	if r := m.runners[id]; r != nil {
		r.Cancel()
		m.gen[id]++
		delete(m.runners, id)
		active = true
	}

	m.tasks = append(m.tasks[:idx], m.tasks[idx+1:]...)
	delete(m.samples, id)
	if deleteFile {
		if active {
			// 文件还被下载协程占用，等它退出后再删
			m.pending[id] = view.SavePath
		} else {
			_ = os.Remove(view.SavePath)
		}
	}
	m.persistLocked()
	return nil
}

// State 返回给前端的完整快照（新任务在前）。
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()

	tasks := make([]TaskDTO, 0, len(m.tasks))
	for i := len(m.tasks) - 1; i >= 0; i-- {
		tasks = append(tasks, m.toDTOLocked(m.tasks[i]))
	}

	var stats GlobalStats
	stats.TotalTasks = len(tasks)
	for _, t := range tasks {
		stats.DownloadSpeed += t.Speed
		if t.Status == model.StatusDownloading {
			stats.ActiveTasks++
		}
	}
	return State{Tasks: tasks, Global: stats, Config: m.cfg}
}

// Config 返回当前配置。
func (m *Manager) Config() config.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// UpdateConfig 更新配置并立即生效（限速、代理、并发数）。
func (m *Manager) UpdateConfig(c config.Config) (config.Config, error) {
	c.Normalize()

	m.mu.Lock()
	defer m.mu.Unlock()

	oldProxy := m.cfg.Proxy
	m.cfg = c
	m.global.SetRate(c.GlobalSpeedLimit)
	if c.Proxy != oldProxy {
		m.client = engine.NewClient(c.Proxy)
	}
	if err := c.Save(m.cfgPath); err != nil {
		return m.cfg, err
	}
	m.scheduleLocked()
	return m.cfg, nil
}

// Probe 供界面在添加任务前预览文件信息。
func (m *Manager) Probe(rawURL string) (*engine.Meta, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("请填写有效的 http/https 地址")
	}
	m.mu.Lock()
	client := m.client
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	return engine.Probe(ctx, client, u.String())
}

// Reveal 在资源管理器中定位文件。
func (m *Manager) Reveal(id string) error {
	m.mu.Lock()
	t := m.findLocked(id)
	var target string
	if t != nil {
		target = t.View().SavePath
	}
	m.mu.Unlock()
	if target == "" {
		return ErrTaskNotFound
	}
	if _, err := os.Stat(target); err != nil {
		target = filepath.Dir(target)
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer", "/select,"+target).Start()
	case "darwin":
		return exec.Command("open", "-R", target).Start()
	default:
		return exec.Command("xdg-open", filepath.Dir(target)).Start()
	}
}

// ---------- 内部方法（调用方必须持有 m.mu） ----------

func (m *Manager) findLocked(id string) *model.Task {
	for _, t := range m.tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (m *Manager) toDTOLocked(t *model.Task) TaskDTO {
	st := t.View()
	speed := int64(0)
	if st.Status == model.StatusDownloading {
		if s, ok := m.samples[st.ID]; ok {
			speed = s.speed
		}
	}
	eta := int64(0)
	if speed > 0 && st.Size > st.Downloaded {
		eta = (st.Size - st.Downloaded) / speed
	}
	progress := 0.0
	if st.Size > 0 {
		progress = float64(st.Downloaded) / float64(st.Size) * 100
		if progress > 100 {
			progress = 100
		}
	} else if st.Status == model.StatusCompleted {
		progress = 100
	}
	return TaskDTO{
		TaskState:  st,
		Speed:      speed,
		ETASeconds: eta,
		Progress:   progress,
		StatusText: st.Status.Text(),
	}
}

func (m *Manager) scheduleLocked() {
	for _, t := range m.tasks {
		if m.active >= m.cfg.MaxConcurrentTasks {
			return
		}
		st := t.View()
		if st.Status != model.StatusQueued {
			continue
		}

		m.active++
		m.gen[st.ID]++
		gen := m.gen[st.ID]

		t.Update(func(s *model.TaskState) {
			s.Status = model.StatusDownloading
			s.Error = ""
		})

		runner := engine.NewRunner(context.Background(), t, m.client, m.global, func(model.TaskState) {
			m.persist()
		})
		runner.Gen = gen
		m.runners[st.ID] = runner

		go func(id string, r *engine.Runner) {
			err := r.Run()
			m.finish(id, r, err)
		}(st.ID, runner)
	}
}

func (m *Manager) finish(id string, r *engine.Runner, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.active--
	if m.active < 0 {
		m.active = 0
	}

	if path, ok := m.pending[id]; ok {
		_ = os.Remove(path)
		delete(m.pending, id)
	}

	stale := m.gen[id] != r.Gen
	t := m.findLocked(id)
	if !stale && t != nil {
		delete(m.runners, id)
		now := time.Now()
		switch {
		case err == nil:
			t.Update(func(s *model.TaskState) {
				s.Status = model.StatusCompleted
				s.CompletedAt = &now
				s.Error = ""
			})
		case errors.Is(err, engine.ErrPaused):
			t.Update(func(s *model.TaskState) {
				s.Status = model.StatusPaused
				s.Error = ""
			})
		case errors.Is(err, engine.ErrCanceled), errors.Is(err, context.Canceled):
			t.Update(func(s *model.TaskState) {
				s.Status = model.StatusCanceled
			})
		default:
			t.Update(func(s *model.TaskState) {
				s.Status = model.StatusFailed
				s.Error = err.Error()
			})
		}
	}

	m.persistLocked()
	m.scheduleLocked()
}

func (m *Manager) persist() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.persistLocked()
}

func (m *Manager) persistLocked() {
	states := make([]model.TaskState, 0, len(m.tasks))
	for _, t := range m.tasks {
		states = append(states, t.View())
	}
	if err := m.store.Save(states); err != nil {
		log.Printf("保存任务进度失败: %v", err)
	}
}

func (m *Manager) samplingLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.Lock()
		now := time.Now()
		active := false
		for _, t := range m.tasks {
			st := t.View()
			if st.Status != model.StatusDownloading {
				delete(m.samples, st.ID)
				continue
			}
			active = true
			prev, ok := m.samples[st.ID]
			if !ok {
				m.samples[st.ID] = speedSample{bytes: st.Downloaded, at: now}
				continue
			}
			elapsed := now.Sub(prev.at).Seconds()
			if elapsed <= 0 {
				continue
			}
			speed := int64(float64(st.Downloaded-prev.bytes) / elapsed)
			if speed < 0 {
				speed = 0
			}
			m.samples[st.ID] = speedSample{bytes: st.Downloaded, at: now, speed: speed}
		}
		if active {
			m.persistLocked()
		}
		m.mu.Unlock()
	}
}

// ---------- 工具函数 ----------

func newID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func uniquePath(dir, name string) (string, error) {
	name = filepath.Base(name)
	if strings.TrimSpace(name) == "" || name == "." || name == string(filepath.Separator) {
		name = "download.bin"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建保存目录失败: %w", err)
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	candidate := filepath.Join(dir, name)
	for i := 1; i < 10000; i++ {
		_, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
	}
	return "", errors.New("目标目录中同名文件过多")
}
