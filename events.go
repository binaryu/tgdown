package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"
)

// FileEventPayload represents the JSON payload sent to external Webhooks
type FileEventPayload struct {
	Event       string  `json:"event"`        // "file_saved" 或 "download_completed"
	Source      string  `json:"source"`       // "telegram", "aria2", "ytdl", "api"
	FileName    string  `json:"file_name"`    // 文件名
	FilePath    string  `json:"file_path"`    // 本地磁盘绝对路径
	FileSize    int64   `json:"file_size"`    // 字节大小
	HumanSize   string  `json:"human_size"`   // 可读格式，如 "302.25 MiB"
	MediaType   string  `json:"media_type"`   // 媒体类型: "video", "document", "audio"
	ChatID      int64   `json:"chat_id"`      // 触发任务的 Telegram Chat ID
	DurationSec float64 `json:"duration_sec"` // 耗时秒数
	CompletedAt int64   `json:"completed_at"` // Unix 秒级时间戳
}

// EventDispatcher manages outbound Webhook calls and local Hook script execution
type EventDispatcher struct {
	cfg        *Config
	httpClient *http.Client
}

// NewEventDispatcher creates a new EventDispatcher
func NewEventDispatcher(cfg *Config) *EventDispatcher {
	return &EventDispatcher{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Dispatch asynchronously triggers Webhook and local script Hook without blocking the main workflow
func (d *EventDispatcher) Dispatch(payload *FileEventPayload) {
	if payload == nil {
		return
	}
	if payload.CompletedAt == 0 {
		payload.CompletedAt = time.Now().Unix()
	}

	go func() {
		d.triggerWebhook(payload)
		d.triggerHookScript(payload)
	}()
}

// triggerWebhook sends HTTP POST JSON notification to external URL
func (d *EventDispatcher) triggerWebhook(payload *FileEventPayload) {
	if d.cfg.WebhookURL == "" {
		return
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Printf("⚠️ Webhook 序列化失败: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cfg.WebhookURL, bytes.NewReader(jsonData))
	if err != nil {
		log.Printf("⚠️ 构造 Webhook 请求失败: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "tgdown-webhook/1.0")
	if d.cfg.WebhookSecret != "" {
		req.Header.Set("X-Webhook-Secret", d.cfg.WebhookSecret)
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		log.Printf("⚠️ Webhook 推送至 %s 失败: %v", d.cfg.WebhookURL, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Printf("📡 Webhook 事件 [%s] 成功推送至: %s (HTTP %d)", payload.Event, d.cfg.WebhookURL, resp.StatusCode)
	} else {
		log.Printf("⚠️ Webhook 响应异常: %s 返回 HTTP %d", d.cfg.WebhookURL, resp.StatusCode)
	}
}

// triggerHookScript executes local bash/python hook script with environment variables
func (d *EventDispatcher) triggerHookScript(payload *FileEventPayload) {
	hookPath := d.cfg.OnFileSavedHook
	if hookPath == "" {
		return
	}

	if _, err := os.Stat(hookPath); err != nil {
		log.Printf("⚠️ Hook 脚本文件不存在: %s (%v)", hookPath, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, hookPath)
	// 继承父进程环境并注入 TG_* 环境变量
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("TG_EVENT=%s", payload.Event),
		fmt.Sprintf("TG_SOURCE=%s", payload.Source),
		fmt.Sprintf("TG_FILE_PATH=%s", payload.FilePath),
		fmt.Sprintf("TG_FILE_NAME=%s", payload.FileName),
		fmt.Sprintf("TG_FILE_SIZE=%d", payload.FileSize),
		fmt.Sprintf("TG_HUMAN_SIZE=%s", payload.HumanSize),
		fmt.Sprintf("TG_MEDIA_TYPE=%s", payload.MediaType),
		fmt.Sprintf("TG_CHAT_ID=%d", payload.ChatID),
		fmt.Sprintf("TG_COMPLETED_AT=%d", payload.CompletedAt),
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("⚠️ 执行 Hook 脚本 %s 失败: %v | 输出: %s", hookPath, err, string(out))
		return
	}

	log.Printf("🔧 Hook 脚本 %s 执行成功 | 输出截断: %s", hookPath, truncateString(string(out), 100))
}

func truncateString(s string, maxLen int) string {
	s = string(bytes.TrimSpace([]byte(s)))
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
