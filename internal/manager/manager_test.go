package manager_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"swiftdl/internal/config"
	"swiftdl/internal/manager"
	"swiftdl/internal/model"
	"swiftdl/internal/store"
)

func TestTaskLifecycle(t *testing.T) {
	data := make([]byte, 2*1024*1024+321)
	for i := range data {
		data[i] = byte(i % 239)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "sample.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer srv.Close()

	dir := t.TempDir()
	st, err := store.New(filepath.Join(dir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.SaveDir = filepath.Join(dir, "out")
	cfg.MaxConcurrentTasks = 2
	cfg.DefaultThreads = 6
	cfg.Retries = 2
	cfg.RetryDelayMS = 200

	m, err := manager.New(cfg, filepath.Join(dir, "config.json"), st)
	if err != nil {
		t.Fatal(err)
	}

	dto, err := m.Add(manager.AddRequest{URL: srv.URL, Threads: 6})
	if err != nil {
		t.Fatalf("添加任务失败: %v", err)
	}
	if dto.StatusText == "" {
		t.Fatal("缺少状态文案")
	}
	savePath := dto.SavePath
	if filepath.Dir(savePath) != cfg.SaveDir {
		t.Fatalf("保存目录不符: %s", savePath)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		tasks := m.State().Tasks
		if len(tasks) != 1 {
			t.Fatalf("任务数量异常: %d", len(tasks))
		}
		task := tasks[0]
		switch task.Status {
		case model.StatusCompleted:
			got, err := os.ReadFile(savePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, data) {
				t.Fatal("下载内容与源文件不一致")
			}
			if task.Progress < 99.9 {
				t.Fatalf("进度异常: %f", task.Progress)
			}
			return
		case model.StatusFailed:
			t.Fatalf("任务失败: %s", task.Error)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("等待下载完成超时")
}

func TestPauseAndResume(t *testing.T) {
	// 2MB 文件配合极小限速，确保能在下载过程中暂停
	data := make([]byte, 2*1024*1024)
	for i := range data {
		data[i] = byte(i % 233)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "slow.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer srv.Close()

	dir := t.TempDir()
	st, err := store.New(filepath.Join(dir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.SaveDir = filepath.Join(dir, "out")
	cfg.DefaultThreads = 4
	cfg.MaxConcurrentTasks = 1

	m, err := manager.New(cfg, filepath.Join(dir, "config.json"), st)
	if err != nil {
		t.Fatal(err)
	}

	dto, err := m.Add(manager.AddRequest{URL: srv.URL, SpeedLimit: 512 * 1024})
	if err != nil {
		t.Fatal(err)
	}

	// 等待出现进度
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tasks := m.State().Tasks
		if len(tasks) == 1 && tasks[0].Downloaded > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := m.Pause(dto.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)

	paused := m.State().Tasks[0]
	if paused.Status != model.StatusPaused {
		t.Fatalf("暂停后状态错误: %s", paused.Status)
	}
	if paused.Downloaded <= 0 || paused.Downloaded >= paused.Size {
		t.Fatalf("暂停进度异常: %d/%d", paused.Downloaded, paused.Size)
	}

	if err := m.Resume(dto.ID); err != nil {
		t.Fatal(err)
	}

	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		task := m.State().Tasks[0]
		if task.Status == model.StatusCompleted {
			got, err := os.ReadFile(paused.SavePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, data) {
				t.Fatal("续传后内容不一致")
			}
			return
		}
		if task.Status == model.StatusFailed {
			t.Fatalf("续传失败: %s", task.Error)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("续传完成超时")
}
