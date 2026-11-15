package engine

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"swiftdl/internal/model"
)

func testData(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 251)
	}
	return data
}

func testServer(t *testing.T, data []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "sample.bin", time.Unix(1700000000, 0), bytes.NewReader(data))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSplitChunksCoverage(t *testing.T) {
	const size = int64(100*1024*1024 + 7)
	chunks := SplitChunks(size, 16)
	if len(chunks) == 0 {
		t.Fatal("分块为空")
	}

	var total int64
	for i, c := range chunks {
		if c.Index != i {
			t.Fatalf("分块序号错误: 期望 %d 实际 %d", i, c.Index)
		}
		if c.Length() <= 0 {
			t.Fatalf("分块 #%d 长度非法: %d", i, c.Length())
		}
		if i > 0 && c.Start != chunks[i-1].End+1 {
			t.Fatalf("分块 #%d 与前一块不连续", i)
		}
		total += c.Length()
	}
	if total != size {
		t.Fatalf("分块总长度 %d != 文件大小 %d", total, size)
	}
	if chunks[len(chunks)-1].End != size-1 {
		t.Fatalf("末块结束位置错误: %d", chunks[len(chunks)-1].End)
	}
}

func TestProbeAndDownloadWithRanges(t *testing.T) {
	data := testData(3*1024*1024 + 123)
	srv := testServer(t, data)
	client := NewClient("")

	meta, err := Probe(context.Background(), client, srv.URL)
	if err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	if meta.Size != int64(len(data)) {
		t.Fatalf("文件大小不符: %d", meta.Size)
	}
	if !meta.RangeSupported {
		t.Fatal("应识别为支持 Range")
	}

	savePath := filepath.Join(t.TempDir(), "out.bin")
	sum := sha256.Sum256(data)
	task := model.NewTask(model.TaskState{
		ID:             "t1",
		URL:            srv.URL,
		SavePath:       savePath,
		Size:           meta.Size,
		RangeSupported: true,
		Threads:        8,
		Retries:        1,
		RetryDelayMS:   100,
		Chunks:         SplitChunks(meta.Size, 8),
		ExpectedHash:   "sha256:" + hex.EncodeToString(sum[:]),
	})

	r := NewRunner(context.Background(), task, client, nil, func(model.TaskState) {})
	if err := r.Run(); err != nil {
		t.Fatalf("下载失败: %v", err)
	}

	got, err := os.ReadFile(savePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("下载内容与源文件不一致")
	}
	view := task.View()
	if view.Downloaded != int64(len(data)) {
		t.Fatalf("进度统计错误: %d", view.Downloaded)
	}
	if view.ActualHash != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("校验值未写入: %s", view.ActualHash)
	}
}

func TestResumeFromPartialChunks(t *testing.T) {
	data := testData(2*1024*1024 + 999)
	srv := testServer(t, data)
	client := NewClient("")

	savePath := filepath.Join(t.TempDir(), "resume.bin")
	chunks := SplitChunks(int64(len(data)), 4)
	if len(chunks) < 2 {
		t.Fatal("分块数量不足")
	}

	// 模拟上次中断：第一块已写入 1000 字节，第二块完成一半
	half := chunks[1].Length() / 2
	written := make([]byte, len(data))
	copy(written, data[:1000])
	copy(written[chunks[1].Start:], data[chunks[1].Start:chunks[1].Start+half])
	if err := os.WriteFile(savePath, written, 0o644); err != nil {
		t.Fatal(err)
	}
	chunks[0].Done = 1000
	chunks[1].Done = half

	md5sum := md5.Sum(data)
	task := model.NewTask(model.TaskState{
		ID:             "t2",
		URL:            srv.URL,
		SavePath:       savePath,
		Size:           int64(len(data)),
		RangeSupported: true,
		Threads:        4,
		Retries:        1,
		RetryDelayMS:   100,
		Chunks:         chunks,
		ExpectedHash:   "md5:" + hex.EncodeToString(md5sum[:]),
	})

	r := NewRunner(context.Background(), task, client, nil, func(model.TaskState) {})
	if err := r.Run(); err != nil {
		t.Fatalf("续传失败: %v", err)
	}

	got, err := os.ReadFile(savePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("续传后文件内容不一致")
	}
}

func TestVerifyFileMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hash.bin")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	wrong := "sha256:" + hex.EncodeToString(make([]byte, 32))
	if _, err := VerifyFile(path, wrong); err == nil {
		t.Fatal("哈希不一致时应返回错误")
	}
	correct := "sha256:" + hex.EncodeToString(func() []byte { s := sha256.Sum256([]byte("hello")); return s[:] }())
	if _, err := VerifyFile(path, correct); err != nil {
		t.Fatalf("哈希一致时不应报错: %v", err)
	}
}
