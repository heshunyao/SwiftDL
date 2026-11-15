package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"swiftdl/internal/config"
	"swiftdl/internal/manager"
	"swiftdl/internal/server"
	"swiftdl/internal/store"
)

func main() {
	port := flag.Int("port", 8765, "HTTP 监听端口")
	dataDir := flag.String("data", "", "数据目录（默认 ./data）")
	noBrowser := flag.Bool("no-browser", false, "启动后不自动打开浏览器")
	flag.Parse()

	dir := *dataDir
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "."
		}
		dir = filepath.Join(cwd, "data")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}

	cfgPath := filepath.Join(dir, "config.json")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Printf("读取配置失败，改用默认配置: %v", err)
	}

	st, err := store.New(filepath.Join(dir, "tasks.json"))
	if err != nil {
		log.Fatalf("初始化任务存储失败: %v", err)
	}

	mgr, err := manager.New(cfg, cfgPath, st)
	if err != nil {
		log.Fatalf("恢复任务失败: %v", err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	pageURL := "http://" + addr

	log.Printf("SwiftDL 已启动: %s", pageURL)
	log.Printf("数据目录: %s", dir)

	if !*noBrowser {
		go func() {
			time.Sleep(300 * time.Millisecond)
			openBrowser(pageURL)
		}()
	}

	if err := http.ListenAndServe(addr, server.New(mgr).Handler()); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

func openBrowser(target string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	_ = cmd.Start()
}
