package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigLoad(t *testing.T) {
	os.Setenv("BOT_TOKEN", "123456:TEST_TOKEN")
	os.Setenv("ADMIN_ID", "10001, 10002")
	os.Setenv("API_BASE", "http://127.0.0.1:8081/")
	os.Setenv("DOWNLOAD_DIR", "/tmp/test_tg_downloads")
	os.Setenv("TASK_TIMEOUT", "15m")
	os.Setenv("THROTTLE_INTERVAL", "2s")
	os.Setenv("ARIA2_SPLIT", "8")
	defer os.RemoveAll("/tmp/test_tg_downloads")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	if cfg.BotToken != "123456:TEST_TOKEN" {
		t.Errorf("BotToken 不匹配: %s", cfg.BotToken)
	}
	if !cfg.IsAdmin(10001) || !cfg.IsAdmin(10002) {
		t.Errorf("Admin ID 校验失败")
	}
	if cfg.IsAdmin(99999) {
		t.Errorf("非 Admin 被误判为 Admin")
	}
	if cfg.APIBase != "http://127.0.0.1:8081" {
		t.Errorf("APIBase 末尾斜线未剔除: %s", cfg.APIBase)
	}
	if cfg.TaskTimeout != 15*time.Minute {
		t.Errorf("TaskTimeout 不匹配: %v", cfg.TaskTimeout)
	}
	if cfg.ThrottleInterval != 2*time.Second {
		t.Errorf("ThrottleInterval 不匹配: %v", cfg.ThrottleInterval)
	}
	if cfg.Aria2Split != 8 {
		t.Errorf("Aria2Split 不匹配: %d", cfg.Aria2Split)
	}
}

func TestTaskManager(t *testing.T) {
	tm := NewTaskManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 首次获取锁应成功
	if !tm.TryAcquire("任务1", 12345, cancel) {
		t.Fatal("获取互斥锁失败")
	}

	// 状态应为执行中
	busy, desc := tm.GetStatus()
	if !busy || !strings.Contains(desc, "任务1") {
		t.Fatalf("状态异常: busy=%v, desc=%s", busy, desc)
	}

	// 第二次获取锁应被互斥拒绝
	if tm.TryAcquire("任务2", 67890, func() {}) {
		t.Fatal("互斥锁未能阻止并发任务")
	}

	// 取消任务
	cancelled, taskName := tm.CancelActiveTask()
	if !cancelled || taskName != "任务1" {
		t.Fatalf("取消任务失败: cancelled=%v, taskName=%s", cancelled, taskName)
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("Context 未被正确取消")
	}

	// 释放锁
	tm.Release()

	// 状态应重置为空闲
	busy, desc = tm.GetStatus()
	if busy || !strings.Contains(desc, "空闲") {
		t.Fatalf("释放后状态异常: busy=%v, desc=%s", busy, desc)
	}

	// 释放后重新获取应成功
	if !tm.TryAcquire("任务3", 11111, func() {}) {
		t.Fatal("释放后重新获取锁失败")
	}
	tm.Release()
}

func TestAria2ProgressRegex(t *testing.T) {
	cases := []struct {
		line       string
		downloaded string
		total      string
		percent    string
		conns      string
		speed      string
		eta        string
	}{
		{
			line:       "[#e87bc7 12MiB/50MiB(24%) CN:16 DL:3.2MiB ETA:11s]",
			downloaded: "12MiB",
			total:      "50MiB",
			percent:    "24%",
			conns:      "16",
			speed:      "3.2MiB",
			eta:        "11s",
		},
		{
			line:       "[#2089b0 1.5GiB/2.0GiB(75%) CN:8 DL:18.2MiB/s ETA:27s]",
			downloaded: "1.5GiB",
			total:      "2.0GiB",
			percent:    "75%",
			conns:      "8",
			speed:      "18.2MiB/s",
			eta:        "27s",
		},
		{
			line:       "[#123456 500KiB/1.0MiB(50%) CN:1 DL:100KiB]",
			downloaded: "500KiB",
			total:      "1.0MiB",
			percent:    "50%",
			conns:      "1",
			speed:      "100KiB",
			eta:        "",
		},
	}

	for i, tc := range cases {
		matches := aria2ProgressRegex.FindStringSubmatch(tc.line)
		if len(matches) < 7 {
			t.Fatalf("用例 %d 匹配失败: %s", i, tc.line)
		}
		if matches[2] != tc.downloaded || matches[3] != tc.total || matches[4] != tc.percent ||
			matches[5] != tc.conns || matches[6] != tc.speed {
			t.Errorf("用例 %d 字段解析错误: %+v", i, matches)
		}
		if tc.eta != "" && matches[7] != tc.eta {
			t.Errorf("用例 %d ETA 解析错误: %s != %s", i, matches[7], tc.eta)
		}
	}
}

func TestRenderProgressBar(t *testing.T) {
	if renderProgressBar(0, 10) != "░░░░░░░░░░" {
		t.Errorf("0%% 进度条错误: %s", renderProgressBar(0, 10))
	}
	if renderProgressBar(50, 10) != "█████░░░░░" {
		t.Errorf("50%% 进度条错误: %s", renderProgressBar(50, 10))
	}
	if renderProgressBar(100, 10) != "██████████" {
		t.Errorf("100%% 进度条错误: %s", renderProgressBar(100, 10))
	}
}

func TestFormatFileSize(t *testing.T) {
	if formatFileSize(500) != "500 B" {
		t.Errorf("500 B 失败: %s", formatFileSize(500))
	}
	if formatFileSize(1024*1024*50) != "50.00 MiB" {
		t.Errorf("50 MiB 失败: %s", formatFileSize(1024*1024*50))
	}
	if formatFileSize(1024*1024*1024*2) != "2.00 GiB" {
		t.Errorf("2 GiB 失败: %s", formatFileSize(1024*1024*1024*2))
	}
}

func TestParseArgs(t *testing.T) {
	raw := `-I https://example.com -H "Authorization: Bearer token123" --header 'X-Custom: val'`
	args := parseArgs(raw)

	expected := []string{
		"-I",
		"https://example.com",
		"-H",
		"Authorization: Bearer token123",
		"--header",
		"X-Custom: val",
	}

	if len(args) != len(expected) {
		t.Fatalf("参数数量不符: 期望 %d, 实际 %d (%+v)", len(expected), len(args), args)
	}

	for i, v := range expected {
		if args[i] != v {
			t.Errorf("参数 %d 不匹配: 期望 %q, 实际 %q", i, v, args[i])
		}
	}
}

func TestFindDownloadedFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test_download_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 创建一个 .aria2 控制文件和一个真实目标文件
	_ = os.WriteFile(filepath.Join(tmpDir, "video.mp4.aria2"), []byte("aria2 metadata"), 0644)
	targetContent := []byte("hello world video stream data")
	_ = os.WriteFile(filepath.Join(tmpDir, "video.mp4"), targetContent, 0644)

	path, size, err := findDownloadedFile(tmpDir)
	if err != nil {
		t.Fatalf("查找文件失败: %v", err)
	}

	if filepath.Base(path) != "video.mp4" {
		t.Errorf("目标文件名错误: %s", filepath.Base(path))
	}
	if size != int64(len(targetContent)) {
		t.Errorf("目标文件大小错误: %d", size)
	}
}
