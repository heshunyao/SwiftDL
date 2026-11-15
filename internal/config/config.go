package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Config 是全局配置，持久化到 data/config.json。
type Config struct {
	// SaveDir 默认下载目录。
	SaveDir string `json:"saveDir"`
	// MaxConcurrentTasks 同时下载的任务数。
	MaxConcurrentTasks int `json:"maxConcurrentTasks"`
	// DefaultThreads 新建任务默认分块并发数。
	DefaultThreads int `json:"defaultThreads"`
	// GlobalSpeedLimit 全局限速（字节/秒），0 表示不限速。
	GlobalSpeedLimit int64 `json:"globalSpeedLimit"`
	// Retries 分块下载失败重试次数。
	Retries int `json:"retries"`
	// RetryDelayMS 首次重试间隔（毫秒），后续按指数退避。
	RetryDelayMS int `json:"retryDelayMs"`
	// Proxy 代理地址，例如 http://127.0.0.1:7890，留空则走系统环境变量。
	Proxy string `json:"proxy"`
}

// Default 返回默认配置。
func Default() Config {
	return Config{
		SaveDir:            defaultSaveDir(),
		MaxConcurrentTasks: 3,
		DefaultThreads:     16,
		GlobalSpeedLimit:   0,
		Retries:            3,
		RetryDelayMS:       1000,
		Proxy:              "",
	}
}

func defaultSaveDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Downloads")
}

// Load 读取配置，文件不存在时写入默认配置。
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if serr := cfg.Save(path); serr != nil {
				return cfg, serr
			}
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), err
	}
	cfg.Normalize()
	return cfg, nil
}

// Save 原子写入配置文件。
func (c Config) Save(path string) error {
	c.Normalize()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Normalize 修正越界或空值，保证运行期配置合法。
func (c *Config) Normalize() {
	if strings.TrimSpace(c.SaveDir) == "" {
		c.SaveDir = defaultSaveDir()
	}
	if c.MaxConcurrentTasks < 1 {
		c.MaxConcurrentTasks = 1
	}
	if c.MaxConcurrentTasks > 16 {
		c.MaxConcurrentTasks = 16
	}
	if c.DefaultThreads < 1 {
		c.DefaultThreads = 1
	}
	if c.DefaultThreads > 128 {
		c.DefaultThreads = 128
	}
	if c.GlobalSpeedLimit < 0 {
		c.GlobalSpeedLimit = 0
	}
	if c.Retries < 0 {
		c.Retries = 0
	}
	if c.Retries > 20 {
		c.Retries = 20
	}
	if c.RetryDelayMS < 100 {
		c.RetryDelayMS = 100
	}
	if c.RetryDelayMS > 60000 {
		c.RetryDelayMS = 60000
	}
	c.Proxy = strings.TrimSpace(c.Proxy)
}
