package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var aria2ProgressRegex = regexp.MustCompile(`\[#([a-f0-9]+)\s+([0-9.]+[A-Za-z]+)\/([0-9.]+[A-Za-z]+)\(([0-9]+%)\)\s+CN:(\d+)\s+DL:([0-9.]+[A-Za-z/]+)(?:\s+ETA:([0-9A-Za-z]+))?\]`)

// Downloader manages aria2c download and Telegram upload transfer
type Downloader struct {
	cfg *Config
	bot *TelegramClient
}

// NewDownloader creates a new downloader instance
func NewDownloader(cfg *Config, bot *TelegramClient) *Downloader {
	return &Downloader{
		cfg: cfg,
		bot: bot,
	}
}

// DownTaskParams holds parsed parameters for /down command
type DownTaskParams struct {
	URL        string
	CustomName string
	Headers    []string
	SendAs     string // "auto", "video", "doc", "audio"
}

// ParseDownArgs parses the argument string of /down, supporting URL, custom name, and -H/--header/--cookie
func ParseDownArgs(rawInput string) (*DownTaskParams, error) {
	tokens := parseArgs(rawInput)
	if len(tokens) == 0 {
		return nil, errors.New("缺少下载链接")
	}

	params := &DownTaskParams{
		SendAs: "auto",
	}
	var positional []string

	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		lower := strings.ToLower(token)

		// 匹配发送模式: --video / --doc / --audio
		if token == "--video" || token == "--as=video" || token == "--media" {
			params.SendAs = "video"
			continue
		}
		if token == "--doc" || token == "--document" || token == "--file" || token == "--as=doc" || token == "--as=file" {
			params.SendAs = "doc"
			continue
		}
		if token == "--audio" || token == "--as=audio" {
			params.SendAs = "audio"
			continue
		}

		// 匹配 -H "Header: Value" 或 --header "Header: Value"
		if token == "-H" || token == "--header" {
			if i+1 >= len(tokens) {
				return nil, errors.New("`-H / --header` 缺少对应的 Header 键值对")
			}
			headerVal := sanitizeHeader(tokens[i+1])
			if headerVal != "" {
				params.Headers = append(params.Headers, headerVal)
			}
			i++
			continue
		} else if strings.HasPrefix(lower, "--header=") {
			val := sanitizeHeader(token[len("--header="):])
			if val != "" {
				params.Headers = append(params.Headers, val)
			}
			continue
		}

		// 匹配 --cookie "key=val" 或 -b "key=val"
		if token == "--cookie" || token == "-b" {
			if i+1 >= len(tokens) {
				return nil, errors.New("`--cookie` 缺少对应的 Cookie 字符串")
			}
			cookieVal := sanitizeHeader(tokens[i+1])
			if cookieVal != "" {
				params.Headers = append(params.Headers, "Cookie: "+cookieVal)
			}
			i++
			continue
		} else if strings.HasPrefix(lower, "--cookie=") {
			cookieVal := sanitizeHeader(token[len("--cookie="):])
			if cookieVal != "" {
				params.Headers = append(params.Headers, "Cookie: "+cookieVal)
			}
			continue
		}

		// 匹配 --referer "https://..." 或 -e "https://..."
		if token == "--referer" || token == "-e" {
			if i+1 >= len(tokens) {
				return nil, errors.New("`--referer` 缺少对应的 Referer 地址")
			}
			refVal := sanitizeHeader(tokens[i+1])
			if refVal != "" {
				params.Headers = append(params.Headers, "Referer: "+refVal)
			}
			i++
			continue
		} else if strings.HasPrefix(lower, "--referer=") {
			refVal := sanitizeHeader(token[len("--referer="):])
			if refVal != "" {
				params.Headers = append(params.Headers, "Referer: "+refVal)
			}
			continue
		}

		// 其他参数视为位置参数 (URL 或自定义文件名)
		positional = append(positional, token)
	}

	if len(positional) == 0 {
		return nil, errors.New("未检测到有效的 URL 链接")
	}

	params.URL = positional[0]
	if len(positional) > 1 {
		params.CustomName = positional[1]
	}

	return params, nil
}

func sanitizeHeader(h string) string {
	h = strings.TrimSpace(h)
	// 剔除换行符，防止 HTTP 头部注入
	h = strings.ReplaceAll(h, "\r", "")
	h = strings.ReplaceAll(h, "\n", "")
	return h
}

// DownloadAndTransfer handles aria2c download, progress throttling, and local file dispatch
func (d *Downloader) DownloadAndTransfer(ctx context.Context, chatID int64, params *DownTaskParams) error {
	targetURL := params.URL
	customName := params.CustomName
	// 1. 创建任务独立临时目录 (确保隔离与原子清理)
	taskID := fmt.Sprintf("down_%d", time.Now().UnixNano())
	taskDir := filepath.Join(d.cfg.DownloadDir, taskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return fmt.Errorf("创建任务目录失败: %w", err)
	}

	// 核心安全约束 4：磁盘自清理，无论成功/失败/撤销均强制删除
	defer func() {
		_ = os.RemoveAll(taskDir)
	}()

	// 2. 检查 aria2c 是否存在
	if _, err := exec.LookPath("aria2c"); err != nil {
		errMsg := "❌ 宿主机未安装 aria2c，请先在终端执行: apt update && apt install -y aria2"
		_, _ = d.bot.SendMessage(ctx, chatID, errMsg, "")
		return errors.New(errMsg)
	}

	// 发送初始状态消息
	initMsg, err := d.bot.SendMessage(ctx, chatID, "⏳ 正在初始化 aria2c 下载任务...", "")
	if err != nil {
		return fmt.Errorf("发送初始消息失败: %w", err)
	}
	statusMsgID := initMsg.MessageID

	// 3. 构造 aria2c 参数
	args := []string{
		"--dir=" + taskDir,
		fmt.Sprintf("-x%d", d.cfg.Aria2Split),
		fmt.Sprintf("-s%d", d.cfg.Aria2Split),
		"-k1M",
		"-c",                     // 断点续传
		"--disk-cache=4M",        // 限制磁盘缓存为 4MB，防止内存暴涨
		"--file-allocation=none", // 禁用预分配，节省内存与 IO
		"--summary-interval=1",
		"--console-log-level=warn",
		"--auto-file-renaming=false",
		"--allow-overwrite=true",
		"--stderr=false", // 输出全部导向 stdout 便于捕获
	}

	if customName != "" {
		// 校验并清理自定义文件名，防止目录穿越
		cleanName := filepath.Base(customName)
		if cleanName != "" && cleanName != "." && cleanName != "/" {
			args = append(args, "--out="+cleanName)
		}
	}

	// 注入自定义请求头 (例如 Cookie、Authorization: Bearer 等)
	for _, h := range params.Headers {
		args = append(args, "--header="+h)
	}

	args = append(args, targetURL)

	// 核心安全约束 5：绑定 Context 外部子进程
	cmd := exec.CommandContext(ctx, "aria2c", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("绑定 aria2c 标准输出失败: %w", err)
	}
	cmd.Stderr = cmd.Stdout // 混合输出

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 aria2c 失败: %w", err)
	}

	// 4. 解析进度并节流推送到 Telegram
	lastUpdate := time.Now()
	var lastReportedText string

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		matches := aria2ProgressRegex.FindStringSubmatch(line)
		if len(matches) >= 7 {
			downloaded := matches[2]
			total := matches[3]
			percent := matches[4]
			conns := matches[5]
			speed := matches[6]
			eta := matches[7]
			if eta == "" {
				eta = "计算中"
			}

			pctInt := 0
			if p, err := strconv.Atoi(strings.TrimSuffix(percent, "%")); err == nil {
				pctInt = p
			}
			bar := renderProgressBar(pctInt, 10)

			progressText := fmt.Sprintf(
				"📥 **下载进度**: `%s` %s\n"+
					"📊 **已完成**: `%s / %s`\n"+
					"⚡ **实时速度**: `%s`\n"+
					"⏳ **预计剩余**: `%s`\n"+
					"🔗 **连接数**: `%s`",
				bar, percent, downloaded, total, speed, eta, conns,
			)

			// 核心约束 3：节流机制 (至少每 2.5 ~ 3 秒更新一次)
			now := time.Now()
			if now.Sub(lastUpdate) >= d.cfg.ThrottleInterval && progressText != lastReportedText {
				_, _ = d.bot.EditMessageText(ctx, chatID, statusMsgID, progressText, "Markdown")
				lastUpdate = now
				lastReportedText = progressText
			}
		}
	}

	// 等待 aria2c 执行完成
	if err := cmd.Wait(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			_, _ = d.bot.EditMessageText(context.Background(), chatID, statusMsgID, "🛑 任务已由用户手动取消。", "")
			return errors.New("任务已被取消")
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			_, _ = d.bot.EditMessageText(context.Background(), chatID, statusMsgID, "⏰ 下载任务已超时。", "")
			return errors.New("下载任务超时")
		}
		errMsg := fmt.Sprintf("❌ aria2c 下载异常退出: %v", err)
		_, _ = d.bot.EditMessageText(context.Background(), chatID, statusMsgID, errMsg, "")
		return errors.New(errMsg)
	}

	// 5. 扫描下载完成的文件 (排除 .aria2 控制文件)
	targetFile, fileSize, err := findDownloadedFile(taskDir)
	if err != nil {
		errMsg := fmt.Sprintf("❌ 未找到下载完成的文件: %v", err)
		_, _ = d.bot.EditMessageText(ctx, chatID, statusMsgID, errMsg, "")
		return errors.New(errMsg)
	}

	// 6. 确定转存模式 (video / audio / doc)
	humanSize := formatFileSize(fileSize)
	fileName := filepath.Base(targetFile)
	ext := strings.ToLower(filepath.Ext(targetFile))

	sendMode := params.SendAs
	if sendMode == "" || sendMode == "auto" {
		switch ext {
		case ".mp4", ".m4v", ".mov", ".webm", ".mkv":
			sendMode = "video"
		case ".mp3", ".flac", ".m4a", ".aac", ".ogg", ".wav", ".opus":
			sendMode = "audio"
		default:
			sendMode = "doc"
		}
	}

	// 核心架构 2：使用 file:/// 协议，严禁将大文件读入 Bot 进程内存
	// 如果配置了容器内映射目录 (例如 Docker 挂载)，自动转换宿主机路径为容器内部路径
	sendPath := targetFile
	if d.cfg.ContainerDownloadDir != "" && d.cfg.ContainerDownloadDir != d.cfg.DownloadDir {
		if rel, err := filepath.Rel(d.cfg.DownloadDir, targetFile); err == nil {
			sendPath = filepath.Join(d.cfg.ContainerDownloadDir, rel)
		}
	}

	fileURI := "file://" + sendPath
	caption := fmt.Sprintf("📦 %s (%s)", fileName, humanSize)

	// 根据模式执行分发，若视频/音频格式异常则自动安全降级为文档文件转存
	var sendErr error
	if sendMode == "video" {
		transferringText := fmt.Sprintf("🚀 下载完成！\n📦 视频文件: %s\n💾 大小: %s\n正在作为 [流式播放视频] 转存至 Telegram...", fileName, humanSize)
		_, _ = d.bot.EditMessageText(ctx, chatID, statusMsgID, transferringText, "")

		_, sendErr = d.bot.SendVideo(ctx, chatID, fileURI, caption, "")
		if sendErr != nil {
			// 如果由于某些特殊编码导致 sendVideo 报错，自动优雅降级为 sendDocument
			_, sendErr = d.bot.SendDocument(ctx, chatID, fileURI, caption, "")
		}
	} else if sendMode == "audio" {
		transferringText := fmt.Sprintf("🚀 下载完成！\n📦 音频文件: %s\n💾 大小: %s\n正在作为 [音频媒体] 转存至 Telegram...", fileName, humanSize)
		_, _ = d.bot.EditMessageText(ctx, chatID, statusMsgID, transferringText, "")

		_, sendErr = d.bot.SendAudio(ctx, chatID, fileURI, caption, "")
		if sendErr != nil {
			_, sendErr = d.bot.SendDocument(ctx, chatID, fileURI, caption, "")
		}
	} else {
		transferringText := fmt.Sprintf("🚀 下载完成！\n📦 文档文件: %s\n💾 大小: %s\n正在作为 [原始文档] 转存至 Telegram...", fileName, humanSize)
		_, _ = d.bot.EditMessageText(ctx, chatID, statusMsgID, transferringText, "")

		_, sendErr = d.bot.SendDocument(ctx, chatID, fileURI, caption, "")
	}

	if sendErr != nil {
		errMsg := fmt.Sprintf("❌ Telegram Local API 转存文件失败: %v", sendErr)
		_, _ = d.bot.EditMessageText(ctx, chatID, statusMsgID, errMsg, "")
		return errors.New(errMsg)
	}

	// 7. 转存完成通知
	finishText := fmt.Sprintf("✅ 转存成功！\n📦 %s (%s)\n🧹 本地临时文件已自动清理。", fileName, humanSize)
	_, _ = d.bot.EditMessageText(ctx, chatID, statusMsgID, finishText, "")

	return nil
}

// findDownloadedFile locates the downloaded file in the task directory
func findDownloadedFile(dir string) (string, int64, error) {
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
		if strings.HasSuffix(name, ".aria2") {
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
		return "", 0, errors.New("任务目录中未找到有效文件")
	}

	absPath, err := filepath.Abs(foundPath)
	if err != nil {
		return "", 0, err
	}

	return absPath, maxSizeBytes, nil
}

// renderProgressBar generates an ASCII progress bar
func renderProgressBar(percent int, totalBlocks int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := (percent * totalBlocks) / 100
	empty := totalBlocks - filled
	if empty < 0 {
		empty = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", empty)
}

// formatFileSize formats byte count to human readable string
func formatFileSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
