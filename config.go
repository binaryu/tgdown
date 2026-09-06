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
	BotToken         string
	AdminIDs         map[int64]struct{}
	APIBase          string
	DownloadDir          string
	ContainerDownloadDir string
	TaskTimeout          time.Duration
	ThrottleInterval time.Duration
	Aria2Split       int
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

	return &Config{
		BotToken:         token,
		AdminIDs:         adminIDs,
		APIBase:          apiBase,
		DownloadDir:          absDownloadDir,
		ContainerDownloadDir: containerDownloadDir,
		TaskTimeout:          taskTimeout,
		ThrottleInterval: throttleInterval,
		Aria2Split:       aria2Split,
	}, nil
}

// IsAdmin checks if a user ID is in the admin whitelist
func (c *Config) IsAdmin(userID int64) bool {
	_, ok := c.AdminIDs[userID]
	return ok
}
