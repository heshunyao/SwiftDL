package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"swiftdl/internal/model"
)

// Store 用单个 JSON 文件保存全部任务（含分块进度），进程重启后可恢复。
type Store struct {
	path string
	mu   sync.Mutex
}

// New 创建存储，path 通常为 data/tasks.json。
func New(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &Store{path: path}, nil
}

// Load 读取全部任务，文件不存在时返回空列表。
func (s *Store) Load() ([]model.TaskState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var tasks []model.TaskState
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

// Save 原子写入全部任务，避免断电导致元数据损坏。
func (s *Store) Save(tasks []model.TaskState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if tasks == nil {
		tasks = []model.TaskState{}
	}
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
