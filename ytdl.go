package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ytDlpProgressRegex = regexp.MustCompile(`\[download\]\s+([0-9.]+)%\s+of\s+([~0-9.]+[A-Za-z]+)(?:\s+at\s+([0-9.]+[A-Za-z/]+))?(?:\s+ETA\s+([0-9:]+))?`)

// YtDownloader handles external yt-dlp binary execution
type YtDownloader struct {
	cfg *Config
	bot *TelegramClient
}

// NewYtDownloader creates a new yt-dlp manager
func NewYtDownloader(cfg *Config, bot *TelegramClient) *YtDownloader {
	return &YtDownloader{
		cfg: cfg,
		bot: bot,
	}
}

// YtTaskParams holds parsed parameters for /ytdl command
type YtTaskParams struct {
	URL        string
	ForceDoc   bool
	Resolution string // e.g. "720", "1080", or ""
}

// ParseYtArgs parses arguments for /ytdl command
func ParseYtArgs(rawInput string) (*YtTaskParams, error) {
	tokens := parseArgs(rawInput)
	if len(tokens) == 0 {
		return nil, errors.New("缺少视频链接")
	}

	params := &YtTaskParams{}

	for _, token := range tokens {
		lower := strings.ToLower(token)
		if lower == "--doc" || lower == "--document" || lower == "--file" {
			params.ForceDoc = true
			continue
		}
		if lower == "720" || lower == "720p" {
			params.Resolution = "720"
			continue
		}
		if lower == "1080" || lower == "1080p" {
			params.Resolution = "1080"
			continue
		}
		if lower == "1440" || lower == "1440p" || lower == "2k" {
			params.Resolution = "1440"
			continue
		}
		if lower == "2160" || lower == "2160p" || lower == "4k" {
			params.Resolution = "2160"
			continue
		}
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			params.URL = token
			continue
		}
	}

	if params.URL == "" {
		return nil, errors.New("未检测到有效的视频链接 (http/https)")
	}

	return params, nil
}

// DownloadAndTransfer invokes host's yt-dlp binary, parses progress, and transfers via Local API
func (y *YtDownloader) DownloadAndTransfer(ctx context.Context, chatID int64, params *YtTaskParams) error {
	// 1. 探测宿主机是否存在 yt-dlp
	ytPath, err := exec.LookPath("yt-dlp")
	if err != nil {
		errMsg := "❌ 宿主机未安装 `yt-dlp`。\n如需使用，请在服务器执行安装:\n`wget https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -O /usr/local/bin/yt-dlp && chmod +x /usr/local/bin/yt-dlp`"
		_, _ = y.bot.SendMessage(ctx, chatID, errMsg, "Markdown")
		return errors.New("宿主机缺少 yt-dlp")
	}

	// 2. 创建任务独立临时目录 (确保隔离与原子清理)
	taskID := fmt.Sprintf("down_yt_%d", time.Now().UnixNano())
	taskDir := filepath.Join(y.cfg.DownloadDir, taskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return fmt.Errorf("创建任务目录失败: %w", err)
	}

	// 核心安全约束：磁盘自清理
	defer func() {
		_ = os.RemoveAll(taskDir)
	}()

	initMsg, err := y.bot.SendMessage(ctx, chatID, "⏳ 正在解析流媒体信息 (yt-dlp)...", "")
	if err != nil {
		return fmt.Errorf("发送初始消息失败: %w", err)
	}
	statusMsgID := initMsg.MessageID

	// 3. 构造 yt-dlp 命令行参数 (支持动态画质与文件大小配置)
	formatStr := "bv*+ba/b/best" // 默认不限制画质，拉取最佳画质
	if params.Resolution != "" && params.Resolution != "0" {
		formatStr = fmt.Sprintf("bv*[height<=%s]+ba/b[height<=%s]/best", params.Resolution, params.Resolution)
	} else if y.cfg.YtdlMaxHeight != "" && y.cfg.YtdlMaxHeight != "0" {
		formatStr = fmt.Sprintf("bv*[height<=%s]+ba/b[height<=%s]/best", y.cfg.YtdlMaxHeight, y.cfg.YtdlMaxHeight)
	}

	args := []string{
		"--newline",     // 强制换行输出，便于标准流实时解析
		"--no-playlist", // 禁止误下整个播放列表
		"-f", formatStr,
		"-o", filepath.Join(taskDir, "%(title).80B [%(id)s].%(ext)s"),
	}

	if y.cfg.MaxFileSize != "" && y.cfg.MaxFileSize != "0" {
		args = append(args, "--max-filesize", y.cfg.MaxFileSize)
	}

	// 若检测到 ffmpeg，开启纯封装混流 (严禁重编码，保护 1 核 CPU)
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		args = append(args, "--remux-video", "mp4")
	}

	args = append(args, params.URL)

	cmd := exec.CommandContext(ctx, ytPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("绑定标准输出失败: %w", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 yt-dlp 失败: %w", err)
	}

	lastUpdate := time.Now()
	var lastReportedText string

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// 混流或后处理状态提示
		if strings.HasPrefix(line, "[Merger]") || strings.HasPrefix(line, "[VideoRemuxer]") {
			now := time.Now()
			if now.Sub(lastUpdate) >= y.cfg.ThrottleInterval {
				_, _ = y.bot.EditMessageText(ctx, chatID, statusMsgID, "⚙️ 正在无损混流封装音视频，即将完成...", "")
				lastUpdate = now
			}
			continue
		}

		matches := ytDlpProgressRegex.FindStringSubmatch(line)
		if len(matches) >= 3 {
			pctStr := matches[1]
			totalSize := matches[2]
			speed := matches[3]
			if speed == "" {
				speed = "计算中"
			}
			eta := matches[4]
			if eta == "" {
				eta = "计算中"
			}

			pctFloat, _ := strconv.ParseFloat(pctStr, 64)
			bar := renderProgressBar(int(pctFloat), 10)

			progressText := fmt.Sprintf(
				"🎬 **流媒体提取**: `%s` %.1f%%\n"+
					"📊 **预估体积**: `%s`\n"+
					"⚡ **实时速度**: `%s`\n"+
					"⏳ **预计剩余**: `%s`",
				bar, pctFloat, totalSize, speed, eta,
			)

			now := time.Now()
			if now.Sub(lastUpdate) >= y.cfg.ThrottleInterval && progressText != lastReportedText {
				_, _ = y.bot.EditMessageText(ctx, chatID, statusMsgID, progressText, "Markdown")
				lastUpdate = now
				lastReportedText = progressText
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			_, _ = y.bot.EditMessageText(context.Background(), chatID, statusMsgID, "🛑 视频提取任务已由用户手动取消。", "")
			return errors.New("任务已被取消")
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			_, _ = y.bot.EditMessageText(context.Background(), chatID, statusMsgID, "⏰ 提取任务已超时。", "")
			return errors.New("任务超时")
		}
		errMsg := fmt.Sprintf("❌ yt-dlp 提取异常退出: %v", err)
		_, _ = y.bot.EditMessageText(context.Background(), chatID, statusMsgID, errMsg, "")
		return errors.New(errMsg)
	}

	// 扫描提取完成的视频文件 (排除临时分片文件)
	targetFile, fileSize, err := findYtDownloadedFile(taskDir)
	if err != nil {
		errMsg := fmt.Sprintf("❌ 未找到提取的视频文件: %v", err)
		_, _ = y.bot.EditMessageText(ctx, chatID, statusMsgID, errMsg, "")
		return errors.New(errMsg)
	}

	humanSize := formatFileSize(fileSize)
	fileName := filepath.Base(targetFile)

	sendPath := targetFile
	if y.cfg.ContainerDownloadDir != "" && y.cfg.ContainerDownloadDir != y.cfg.DownloadDir {
		if rel, err := filepath.Rel(y.cfg.DownloadDir, targetFile); err == nil {
			sendPath = filepath.Join(y.cfg.ContainerDownloadDir, rel)
		}
	}

	fileURI := "file://" + sendPath
	caption := fmt.Sprintf("🎬 %s (%s)", fileName, humanSize)

	var sendErr error
	if params.ForceDoc {
		transferringText := fmt.Sprintf("🚀 提取完成！\n📦 视频文件: %s\n💾 大小: %s\n正在作为 [原始文档] 转存至 Telegram...", fileName, humanSize)
		_, _ = y.bot.EditMessageText(ctx, chatID, statusMsgID, transferringText, "")
		_, sendErr = y.bot.SendDocument(ctx, chatID, fileURI, caption, "")
	} else {
		transferringText := fmt.Sprintf("🚀 提取完成！\n📦 视频文件: %s\n💾 大小: %s\n正在作为 [流式播放视频] 转存至 Telegram...", fileName, humanSize)
		_, _ = y.bot.EditMessageText(ctx, chatID, statusMsgID, transferringText, "")
		_, sendErr = y.bot.SendVideo(ctx, chatID, fileURI, caption, "")
		if sendErr != nil {
			log.Printf("⚠️ sendVideo 失败 (%v)，降级为 sendDocument...", sendErr)
			_, sendErr = y.bot.SendDocument(ctx, chatID, fileURI, caption, "")
		}
	}

	if sendErr != nil {
		errMsg := fmt.Sprintf("❌ Telegram Local API 转存失败: %v", sendErr)
		_, _ = y.bot.EditMessageText(ctx, chatID, statusMsgID, errMsg, "")
		return errors.New(errMsg)
	}

	finishText := fmt.Sprintf("✅ 转存成功！\n🎬 %s (%s)\n🧹 本地临时文件已自动清理。", fileName, humanSize)
	_, _ = y.bot.EditMessageText(ctx, chatID, statusMsgID, finishText, "")

	return nil
}

func findYtDownloadedFile(dir string) (string, int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0, err
	}

	var foundPath string
	var maxSizeBytes int64 = -1

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".ytdl") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.Size() > maxSizeBytes {
			maxSizeBytes = info.Size()
			foundPath = filepath.Join(dir, name)
		}
	}

	if foundPath == "" || maxSizeBytes < 0 {
		return "", 0, errors.New("任务目录中未找到有效的视频文件")
	}

	absPath, err := filepath.Abs(foundPath)
	if err != nil {
		return "", 0, err
	}

	return absPath, maxSizeBytes, nil
}

// IsVideoPlatformURL checks if a URL belongs to well-known video platforms that aria2c cannot download directly
func IsVideoPlatformURL(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	domains := []string{
		"youtube.com",
		"youtu.be",
		"bilibili.com",
		"b23.tv",
		"twitter.com",
		"x.com",
		"tiktok.com",
		"douyin.com",
		"instagram.com",
		"facebook.com",
		"fb.watch",
		"reddit.com",
		"vimeo.com",
	}

	for _, d := range domains {
		if strings.Contains(lower, d) {
			return true
		}
	}
	return false
}
