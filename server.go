package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// APIServer exposes lightweight RESTful HTTP endpoints for external tools
type APIServer struct {
	cfg        *Config
	bot        *TelegramClient
	taskMgr    *TaskManager
	downloader *Downloader
	ytdl       *YtDownloader
	saver      *FileSaver
	dispatcher *EventDispatcher
	server     *http.Server
}

// NewAPIServer creates a new API server instance
func NewAPIServer(
	cfg *Config,
	bot *TelegramClient,
	taskMgr *TaskManager,
	downloader *Downloader,
	ytdl *YtDownloader,
	saver *FileSaver,
	dispatcher *EventDispatcher,
) *APIServer {
	return &APIServer{
		cfg:        cfg,
		bot:        bot,
		taskMgr:    taskMgr,
		downloader: downloader,
		ytdl:       ytdl,
		saver:      saver,
		dispatcher: dispatcher,
	}
}

// Start launches the HTTP server in background if APIListenAddr is configured
func (s *APIServer) Start(ctx context.Context) {
	if s.cfg.APIListenAddr == "" {
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", s.wrapAuth(s.handleStatus))
	mux.HandleFunc("/api/v1/download", s.wrapAuth(s.handleDownload))
	mux.HandleFunc("/api/v1/notify", s.wrapAuth(s.handleNotify))
	mux.HandleFunc("/api/v1/cancel", s.wrapAuth(s.handleCancel))

	s.server = &http.Server{
		Addr:         s.cfg.APIListenAddr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		log.Printf("🌐 REST API 服务启动监听: http://%s (外部调用已就绪)", s.cfg.APIListenAddr)
		if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("❌ REST API 服务异常退出: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)
		log.Println("🛑 REST API 服务已优雅关闭")
	}()
}

// wrapAuth enforces API key authentication if APISecretKey is set
func (s *APIServer) wrapAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APISecretKey != "" {
			apiKey := r.Header.Get("X-API-Key")
			if apiKey == "" {
				authHeader := r.Header.Get("Authorization")
				if strings.HasPrefix(authHeader, "Bearer ") {
					apiKey = strings.TrimPrefix(authHeader, "Bearer ")
				}
			}

			if apiKey != s.cfg.APISecretKey {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok":    false,
					"error": "未授权: 无效的 API Key",
				})
				return
			}
		}
		next(w, r)
	}
}

// handleStatus returns current system, task and disk status
func (s *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	busy, taskDesc := s.taskMgr.GetStatus()
	freeDisk, _ := getFreeDiskSpace(s.cfg.LocalSaveDir)

	respData := map[string]any{
		"ok":              true,
		"busy":            busy,
		"current_task":    taskDesc,
		"free_disk_space": formatFileSize(int64(freeDisk)),
		"free_disk_bytes": freeDisk,
		"local_save_dir":  s.cfg.LocalSaveDir,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(respData)
}

// DownloadRequest represents JSON payload for /api/v1/download
type DownloadRequest struct {
	URL        string `json:"url"`
	CustomName string `json:"custom_name,omitempty"`
	SendToTG   bool   `json:"send_to_tg,omitempty"` // 是否转存至 Telegram，默认 false 直接存本地
	ChatID     int64  `json:"chat_id,omitempty"`    // 若 send_to_tg=true，目标 Telegram 会话 ID
	SendAs     string `json:"send_as,omitempty"`    // "auto", "video", "doc"
	TargetSub  string `json:"target_sub,omitempty"` // 本地保存的相对子目录
}

// handleDownload handles external download task submission
func (s *APIServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "请求体 JSON 格式错误",
		})
		return
	}

	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "URL 不能为空",
		})
		return
	}

	taskName := fmt.Sprintf("[API] %s", req.URL)
	taskCtx, cancel := context.WithTimeout(context.Background(), s.cfg.TaskTimeout)

	// 占用全局互斥任务锁
	acquired, taskID := s.taskMgr.TryAcquire(taskName, req.ChatID, cancel)
	if !acquired {
		cancel()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "系统繁忙: 当前有任务正在执行",
		})
		return
	}

	go func() {
		defer s.taskMgr.Release(taskID)
		defer cancel()

		if req.SendToTG && req.ChatID != 0 {
			// 转存至 Telegram
			params := &DownTaskParams{
				URL:        req.URL,
				CustomName: req.CustomName,
				SendAs:     req.SendAs,
			}
			_ = s.downloader.DownloadAndTransfer(taskCtx, req.ChatID, params)
		} else {
			// 直接下载至本地持久化目录（最适合媒体库入库工作流）
			s.runDirectLocalDownload(taskCtx, req)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"task_id": taskID,
		"message": "下载任务已接受并加入执行队列",
	})
}

// runDirectLocalDownload downloads target file directly into LocalSaveDir via aria2c
func (s *APIServer) runDirectLocalDownload(ctx context.Context, req DownloadRequest) {
	startTime := time.Now()
	saveDir := s.cfg.LocalSaveDir
	if req.TargetSub != "" {
		saveDir = filepath.Join(saveDir, filepath.Clean(req.TargetSub))
	}
	_ = os.MkdirAll(saveDir, 0755)

	args := []string{
		"--dir=" + saveDir,
		fmt.Sprintf("--max-connection-per-server=%d", s.cfg.Aria2Split),
		fmt.Sprintf("--split=%d", s.cfg.Aria2Split),
		"--continue=true",
		"--summary-interval=0",
	}
	if req.CustomName != "" {
		args = append(args, "--out="+filepath.Base(req.CustomName))
	}
	args = append(args, req.URL)

	log.Printf("▶️ [API] 开始直接下载至本地: %s -> %s", req.URL, saveDir)
	cmd := exec.CommandContext(ctx, "aria2c", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("❌ [API] aria2c 直接下载失败: %v | %s", err, string(out))
		return
	}

	// 探测最新下载的文件
	downloadedFile, fileSize, err := findDownloadedFile(saveDir)
	if err != nil {
		log.Printf("⚠️ [API] 未找到下载落盘文件: %v", err)
		return
	}

	log.Printf("✅ [API] 文件已直接落盘: %s (%s)", downloadedFile, formatFileSize(fileSize))

	// 触发 Webhook / Hook 脚本通知媒体库
	if s.dispatcher != nil {
		s.dispatcher.Dispatch(&FileEventPayload{
			Event:       "download_completed",
			Source:      "api",
			FileName:    filepath.Base(downloadedFile),
			FilePath:    downloadedFile,
			FileSize:    fileSize,
			HumanSize:   formatFileSize(fileSize),
			MediaType:   guessMediaType(downloadedFile),
			ChatID:      req.ChatID,
			DurationSec: time.Since(startTime).Seconds(),
		})
	}
}

// NotifyRequest represents payload for /api/v1/notify
type NotifyRequest struct {
	ChatID    int64  `json:"chat_id,omitempty"` // 为空时自动发送给第一个 AdminID
	Message   string `json:"message"`
	ParseMode string `json:"parse_mode,omitempty"`
}

// handleNotify allows external services to push notification to Telegram
func (s *APIServer) handleNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	var req NotifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "请求体 JSON 格式错误",
		})
		return
	}

	if strings.TrimSpace(req.Message) == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "通知内容不能为空",
		})
		return
	}

	targetChatID := req.ChatID
	if targetChatID == 0 {
		// 默认取第一个 Admin
		for adminID := range s.cfg.AdminIDs {
			targetChatID = adminID
			break
		}
	}

	if targetChatID == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "未提供有效 chat_id 且未配置有效 ADMIN_ID",
		})
		return
	}

	parseMode := req.ParseMode
	if parseMode == "" {
		parseMode = "Markdown"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	msg, err := s.bot.SendMessage(ctx, targetChatID, req.Message, parseMode)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("发送 Telegram 消息失败: %v", err),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":         true,
		"message_id": msg.MessageID,
	})
}

// handleCancel cancels all running tasks via API
func (s *APIServer) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	count, names := s.taskMgr.CancelAllActiveTasks()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":             true,
		"canceled_count": count,
		"canceled_tasks": names,
	})
}
