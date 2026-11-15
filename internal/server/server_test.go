package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swiftdl/internal/config"
	"swiftdl/internal/manager"
	"swiftdl/internal/model"
	"swiftdl/internal/server"
	"swiftdl/internal/store"
)

func TestAPIFlow(t *testing.T) {
	data := make([]byte, 1024*1024+77)
	for i := range data {
		data[i] = byte(i % 199)
	}
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "sample.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	defer fileSrv.Close()

	dir := t.TempDir()
	st, err := store.New(filepath.Join(dir, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.SaveDir = filepath.Join(dir, "out")
	cfg.MaxConcurrentTasks = 2
	cfg.DefaultThreads = 4

	mgr, err := manager.New(cfg, filepath.Join(dir, "config.json"), st)
	if err != nil {
		t.Fatal(err)
	}
	apiSrv := httptest.NewServer(server.New(mgr).Handler())
	defer apiSrv.Close()

	// 首页应返回前端页面
	resp, err := http.Get(apiSrv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(resp.Body)
	resp.Body.Close()
	if !strings.Contains(body.String(), "SwiftDL") {
		t.Fatal("首页内容异常")
	}

	// 添加任务
	payload, _ := json.Marshal(manager.AddRequest{URL: fileSrv.URL + "/sample.bin", Threads: 4})
	resp, err = http.Post(apiSrv.URL+"/api/tasks", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		resp.Body.Close()
		t.Fatalf("添加任务失败: %s", buf.String())
	}
	var created manager.TaskDTO
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// 轮询直到完成
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(apiSrv.URL + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		var state manager.State
		if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()

		if len(state.Tasks) != 1 {
			t.Fatalf("任务数量异常: %d", len(state.Tasks))
		}
		task := state.Tasks[0]
		if task.Status == model.StatusFailed {
			t.Fatalf("任务失败: %s", task.Error)
		}
		if task.Status == model.StatusCompleted {
			got, err := os.ReadFile(created.SavePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, data) {
				t.Fatal("下载内容不一致")
			}
			if state.Global.TotalTasks != 1 {
				t.Fatalf("全局统计异常: %+v", state.Global)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("等待任务完成超时")
}
