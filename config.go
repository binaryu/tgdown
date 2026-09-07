package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime configuration loaded from environment variables
type Config struct {
	BotToken             string
	AdminIDs             map[int64]struct{}
	AllowedGroupIDs      map[int64]struct{}
	APIBase              string
	DownloadDir          string
	ContainerDownloadDir string
	TaskTimeout          time.Duration
	ThrottleInterval     time.Duration
	Aria2Split           int
	Aria2DiskCache       string
	Aria2FileAlloc       string
	MaxConcurrentTasks   int
	MaxFileSize          string
	YtdlMaxHeight        string
	YtdlProxy            string
	YtdlCookiesFile      string
	BotMemoryLimit       string
	DeleteProgressMsg    bool
}

// loadDotEnv reads .env file from current directory if present
func loadDotEnv() {
	file, err := os.Open(".env")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			val = strings.Trim(val, `"'`)
			if os.Getenv(key) == "" {
				_ = os.Setenv(key, val)
			}
		}
	}
}

// LoadConfig reads and validates configuration from environment variables
func LoadConfig() (*Config, error) {
	loadDotEnv()

	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" {
		return nil, errors.New("环境变量 BOT_TOKEN 不能为空")
	}

	adminRaw := strings.TrimSpace(os.Getenv("ADMIN_ID"))
	if adminRaw == "" {
		return nil, errors.New("环境变量 ADMIN_ID 不能为空 (支持单个或英文逗号分隔的 ID)")
	}

	adminIDs := make(map[int64]struct{})
	for _, part := range strings.Split(adminRaw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("ADMIN_ID 中的 ID '%s' 解析失败: %w", part, err)
		}
		adminIDs[id] = struct{}{}
	}
	if len(adminIDs) == 0 {
		return nil, errors.New("至少需要配置一个有效的 ADMIN_ID")
	}

	groupRaw := strings.TrimSpace(os.Getenv("ALLOWED_GROUP_IDS"))
	allowedGroupIDs := make(map[int64]struct{})
	if groupRaw != "" {
		for _, part := range strings.Split(groupRaw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("ALLOWED_GROUP_IDS 中的群组 ID '%s' 解析失败: %w", part, err)
			}
			allowedGroupIDs[id] = struct{}{}
		}
	}

	apiBase := strings.TrimRight(strings.TrimSpace(os.Getenv("API_BASE")), "/")
	if apiBase == "" {
		apiBase = "http://127.0.0.1:8081"
	}

	downloadDir := strings.TrimSpace(os.Getenv("DOWNLOAD_DIR"))
	if downloadDir == "" {
		downloadDir = "/tmp/tg_downloads"
	}
	absDownloadDir, err := filepath.Abs(downloadDir)
	if err != nil {
		return nil, fmt.Errorf("解析 DOWNLOAD_DIR 绝对路径失败: %w", err)
	}

	// 确保下载目录存在
	if err := os.MkdirAll(absDownloadDir, 0755); err != nil {
		return nil, fmt.Errorf("创建下载目录 %s 失败: %w", absDownloadDir, err)
	}

	containerDownloadDir := strings.TrimSpace(os.Getenv("CONTAINER_DOWNLOAD_DIR"))
	if containerDownloadDir == "" {
		containerDownloadDir = absDownloadDir
	}

	taskTimeout := 30 * time.Minute
	if tStr := strings.TrimSpace(os.Getenv("TASK_TIMEOUT")); tStr != "" {
		d, err := time.ParseDuration(tStr)
		if err != nil {
			return nil, fmt.Errorf("解析 TASK_TIMEOUT 失败: %w", err)
		}
		taskTimeout = d
	}

	throttleInterval := 3 * time.Second
	if throttleStr := strings.TrimSpace(os.Getenv("THROTTLE_INTERVAL")); throttleStr != "" {
		d, err := time.ParseDuration(throttleStr)
		if err != nil {
			return nil, fmt.Errorf("解析 THROTTLE_INTERVAL 失败: %w", err)
		}
		if d < 1*time.Second {
			d = 1 * time.Second
		}
		throttleInterval = d
	}

	aria2Split := 16
	if splitStr := strings.TrimSpace(os.Getenv("ARIA2_SPLIT")); splitStr != "" {
		s, err := strconv.Atoi(splitStr)
		if err == nil && s > 0 && s <= 64 {
			aria2Split = s
		}
	}

	aria2DiskCache := strings.TrimSpace(os.Getenv("ARIA2_DISK_CACHE"))
	if aria2DiskCache == "" {
		aria2DiskCache = "16M" // 默认 16M (标准 aria2 缓存)
	}

	aria2FileAlloc := strings.TrimSpace(os.Getenv("ARIA2_FILE_ALLOC"))
	if aria2FileAlloc == "" {
		aria2FileAlloc = "falloc" // 默认 falloc (Linux 高效分配，若低配机器可指定 none)
	}

	maxConcurrentTasks := 1
	if concStr := strings.TrimSpace(os.Getenv("MAX_CONCURRENT_TASKS")); concStr != "" {
		c, err := strconv.Atoi(concStr)
		if err == nil && c > 0 && c <= 32 {
			maxConcurrentTasks = c
		}
	}

	maxFileSize := strings.TrimSpace(os.Getenv("MAX_FILE_SIZE"))
	if maxFileSize == "" {
		maxFileSize = "2000M" // 默认匹配 Telegram Local API 2000MB 上限
	}

	ytdlMaxHeight := strings.TrimSpace(os.Getenv("YTDL_MAX_HEIGHT"))
	if ytdlMaxHeight == "" {
		ytdlMaxHeight = "0" // 默认 0 (不限制画质，拉取最佳可用画质；低配机器可配 1080/720)
	}

	ytdlProxy := strings.TrimSpace(os.Getenv("YTDL_PROXY"))
	ytdlCookiesFile := strings.TrimSpace(os.Getenv("YTDL_COOKIES_FILE"))
	if ytdlCookiesFile == "" {
		// 检查本地是否存在 cookies.txt
		if _, err := os.Stat("cookies.txt"); err == nil {
			ytdlCookiesFile = "cookies.txt"
		}
	}

	botMemoryLimit := strings.TrimSpace(os.Getenv("BOT_MEMORY_LIMIT"))

	deleteProgressMsg := true
	if delStr := strings.TrimSpace(os.Getenv("DELETE_PROGRESS_MSG")); delStr != "" {
		if strings.ToLower(delStr) == "false" || delStr == "0" {
			deleteProgressMsg = false
		}
	}

	return &Config{
		BotToken:             token,
		AdminIDs:             adminIDs,
		AllowedGroupIDs:      allowedGroupIDs,
		APIBase:              apiBase,
		DownloadDir:          absDownloadDir,
		ContainerDownloadDir: containerDownloadDir,
		TaskTimeout:          taskTimeout,
		ThrottleInterval:     throttleInterval,
		Aria2Split:           aria2Split,
		Aria2DiskCache:       aria2DiskCache,
		Aria2FileAlloc:       aria2FileAlloc,
		MaxConcurrentTasks:   maxConcurrentTasks,
		MaxFileSize:          maxFileSize,
		YtdlMaxHeight:        ytdlMaxHeight,
		YtdlProxy:            ytdlProxy,
		YtdlCookiesFile:      ytdlCookiesFile,
		BotMemoryLimit:       botMemoryLimit,
		DeleteProgressMsg:    deleteProgressMsg,
	}, nil
}

// IsAdmin checks if a user ID is in the admin whitelist
func (c *Config) IsAdmin(userID int64) bool {
	_, ok := c.AdminIDs[userID]
	return ok
}

// IsAllowedGroup checks if a group chat ID is in the allowed whitelist
func (c *Config) IsAllowedGroup(chatID int64) bool {
	_, ok := c.AllowedGroupIDs[chatID]
	return ok
}

// CanAccess checks if the incoming message is authorized (Admin or Allowed Group)
func (c *Config) CanAccess(msg *Message) bool {
	if msg == nil {
		return false
	}
	// 管理员在任何地方均有最高权限
	if msg.From != nil && c.IsAdmin(msg.From.ID) {
		return true
	}
	// 如果是群聊/超级群聊，校验是否在允许群组白名单中
	if msg.Chat != nil && (msg.Chat.Type == "group" || msg.Chat.Type == "supergroup") {
		return c.IsAllowedGroup(msg.Chat.ID)
	}
	return false
}
