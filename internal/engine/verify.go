package engine

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"

	"swiftdl/internal/model"
)

// SplitChunks 按目标并发数把文件切成若干分块。
// 目标块数为并发数的 4 倍，块大小限制在 1MiB ~ 16MiB 之间，
// 这样既能让并发持续有活干，又能避免碎块过多带来的额外请求开销。
func SplitChunks(size int64, threads int) []model.Chunk {
	if size <= 0 {
		return []model.Chunk{{Index: 0, Start: 0, End: 0}}
	}
	if threads < 1 {
		threads = 1
	}
	target := int64(threads) * 4
	chunkSize := size / target
	if chunkSize < 1<<20 {
		chunkSize = 1 << 20
	}
	if chunkSize > 16<<20 {
		chunkSize = 16 << 20
	}

	chunks := make([]model.Chunk, 0, size/chunkSize+1)
	for start := int64(0); start < size; start += chunkSize {
		end := start + chunkSize - 1
		if end >= size {
			end = size - 1
		}
		chunks = append(chunks, model.Chunk{Index: len(chunks), Start: start, End: end})
	}
	return chunks
}

// VerifyFile 计算文件哈希并与期望值比对，返回实际哈希（形如 sha256:xxxx）。
// expected 支持 "sha256:HEX"、"md5:HEX"，或直接给出 64/32 位十六进制串。
func VerifyFile(filePath, expected string) (string, error) {
	algo, want, err := parseHash(expected)
	if err != nil {
		return "", err
	}
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var h hash.Hash
	switch algo {
	case "md5":
		h = md5.New()
	default:
		h = sha256.New()
	}
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	label := algo + ":" + actual
	if !strings.EqualFold(actual, want) {
		return label, fmt.Errorf("%s 校验不一致：期望 %s，实际 %s", algo, strings.ToLower(want), actual)
	}
	return label, nil
}

func parseHash(expected string) (algo string, value string, err error) {
	v := strings.TrimSpace(expected)
	if v == "" {
		return "", "", errors.New("未提供校验值")
	}
	lower := strings.ToLower(v)
	switch {
	case strings.HasPrefix(lower, "sha256:"):
		return "sha256", strings.TrimPrefix(lower, "sha256:"), nil
	case strings.HasPrefix(lower, "md5:"):
		return "md5", strings.TrimPrefix(lower, "md5:"), nil
	case len(lower) == 64:
		return "sha256", lower, nil
	case len(lower) == 32:
		return "md5", lower, nil
	}
	return "", "", fmt.Errorf("无法识别的校验值格式：%s", expected)
}
