package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// MediaInfo encapsulates metadata for downloadable media from Telegram
type MediaInfo struct {
	FileID    string
	FileName  string
	FileSize  int64
	MediaType string
}

// ExtractMediaInfo extracts downloadable media info from a Message
func ExtractMediaInfo(msg *Message) *MediaInfo {
	if msg == nil {
		return nil
	}

	timestamp := time.Now().Format("20060102_150405")

	if msg.Document != nil && msg.Document.FileID != "" {
		name := sanitizeFileName(msg.Document.FileName)
		if name == "" {
			name = fmt.Sprintf("document_%s", timestamp)
		}
		return &MediaInfo{
			FileID:    msg.Document.FileID,
			FileName:  name,
			FileSize:  msg.Document.FileSize,
			MediaType: "文档 (Document)",
		}
	}

	if msg.Video != nil && msg.Video.FileID != "" {
		name := sanitizeFileName(msg.Video.FileName)
		if name == "" {
			name = fmt.Sprintf("video_%s.mp4", timestamp)
		}
		return &MediaInfo{
			FileID:    msg.Video.FileID,
			FileName:  name,
			FileSize:  msg.Video.FileSize,
			MediaType: "视频 (Video)",
		}
	}

	if msg.Audio != nil && msg.Audio.FileID != "" {
		name := sanitizeFileName(msg.Audio.FileName)
		if name == "" {
			if msg.Audio.Title != "" {
				name = sanitizeFileName(msg.Audio.Title) + ".mp3"
			} else {
				name = fmt.Sprintf("audio_%s.mp3", timestamp)
			}
		}
		return &MediaInfo{
			FileID:    msg.Audio.FileID,
			FileName:  name,
			FileSize:  msg.Audio.FileSize,
			MediaType: "音频 (Audio)",
		}
	}

	if msg.Voice != nil && msg.Voice.FileID != "" {
		return &MediaInfo{
			FileID:    msg.Voice.FileID,
			FileName:  fmt.Sprintf("voice_%s.ogg", timestamp),
			FileSize:  msg.Voice.FileSize,
			MediaType: "语音 (Voice)",
		}
	}

	if len(msg.Photo) > 0 {
		// 取最高分辨率的图 (Telegram 数组最后一个)
		bestPhoto := msg.Photo[len(msg.Photo)-1]
		if bestPhoto.FileID != "" {
			return &MediaInfo{
				FileID:    bestPhoto.FileID,
				FileName:  fmt.Sprintf("photo_%s.jpg", timestamp),
				FileSize:  bestPhoto.FileSize,
				MediaType: "图片 (Photo)",
			}
		}
	}

	return nil
}

// sanitizeFileName strips dangerous characters and path traversal segments
func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "\x00", "")
	if name == "." || name == ".." {
		return ""
	}
	return name
}

// resolveDestinationPath computes safe absolute destination path within baseDir
func resolveDestinationPath(baseDir, customInput, defaultFileName string) (string, error) {
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("解析保存根目录失败: %w", err)
	}

	customInput = strings.TrimSpace(customInput)
	var targetRel string

	if customInput == "" {
		targetRel = defaultFileName
	} else if strings.HasSuffix(customInput, "/") || strings.HasSuffix(customInput, "\\") {
		// 显式以斜杠结尾，视为子目录
		targetRel = filepath.Join(filepath.Clean(customInput), defaultFileName)
	} else if filepath.Clean(customInput) == filepath.Base(absBase) || filepath.Clean(customInput) == "." {
		// 用户输入了与保存目录相同的名字 (例如 /save downloads)，意图为保存到根目录
		targetRel = defaultFileName
	} else {
		// 检查在 absBase 下是否已存在同名目录
		checkDir := filepath.Join(absBase, filepath.Clean(customInput))
		if info, sErr := os.Stat(checkDir); sErr == nil && info.IsDir() {
			targetRel = filepath.Join(filepath.Clean(customInput), defaultFileName)
		} else if filepath.Ext(customInput) == "" {
			// 没有文件后缀名，智能判断为意图保存到该子目录
			targetRel = filepath.Join(filepath.Clean(customInput), defaultFileName)
		} else {
			// 带有文件后缀名，视为自定义文件名
			targetRel = filepath.Clean(customInput)
		}
	}

	candidate := filepath.Join(absBase, targetRel)
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("解析目标路径失败: %w", err)
	}

	// 安全防线：防止通过 ../ 逃逸出 baseDir
	rel, err := filepath.Rel(absBase, absCandidate)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return "", errors.New("目标路径非法，禁止目录穿越访问")
	}

	return absCandidate, nil
}

// avoidFileOverwrite generates unique filename if file already exists
func avoidFileOverwrite(filePath string) string {
	if _, err := os.Stat(filePath); errors.Is(err, os.ErrNotExist) {
		return filePath
	}

	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)
	ext := filepath.Ext(base)
	nameWithoutExt := strings.TrimSuffix(base, ext)

	for i := 1; i < 10000; i++ {
		newName := fmt.Sprintf("%s (%d)%s", nameWithoutExt, i, ext)
		newPath := filepath.Join(dir, newName)
		if _, err := os.Stat(newPath); errors.Is(err, os.ErrNotExist) {
			return newPath
		}
	}
	return filePath
}

// getFreeDiskSpace retrieves available disk space in bytes
func getFreeDiskSpace(dir string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

// findLatestGrowingFile searches for the most recently modified downloading file in the Local API data directory
func findLatestGrowingFile(dir string, since time.Time) (int64, string, bool) {
	var latestTime time.Time
	var latestSize int64
	var latestPath string

	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".binlog") || strings.HasPrefix(name, ".") {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		modTime := info.ModTime()
		// 关注任务启动后修改或最近更新的文件
		if modTime.After(since) || modTime.After(time.Now().Add(-10*time.Second)) {
			if modTime.After(latestTime) {
				latestTime = modTime
				latestSize = info.Size()
				latestPath = path
			}
		}
		return nil
	})

	return latestSize, latestPath, latestPath != ""
}

func guessMediaType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".mp4", ".mkv", ".mov", ".avi", ".webm", ".flv", ".ts":
		return "video"
	case ".mp3", ".flac", ".m4a", ".aac", ".ogg", ".wav", ".opus":
		return "audio"
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		return "photo"
	default:
		return "document"
	}
}

// FileSaver handles downloading Telegram media to local VPS storage
type FileSaver struct {
	cfg        *Config
	bot        *TelegramClient
	dispatcher *EventDispatcher
}

// NewFileSaver creates a new FileSaver instance
func NewFileSaver(cfg *Config, bot *TelegramClient, dispatcher *EventDispatcher) *FileSaver {
	return &FileSaver{
		cfg:        cfg,
		bot:        bot,
		dispatcher: dispatcher,
	}
}

// SaveTelegramFile downloads and saves the media file to local disk
func (s *FileSaver) SaveTelegramFile(ctx context.Context, chatID int64, media *MediaInfo, customPath string) error {
	startTime := time.Now()

	// 1. 检查单文件体积限制
	if s.cfg.MaxFileSize != "" && s.cfg.MaxFileSize != "0" {
		limitBytes := parseMemoryBytes(s.cfg.MaxFileSize)
		if limitBytes > 0 && media.FileSize > limitBytes {
			errMsg := fmt.Sprintf("❌ 文件体积 (%s) 超出系统允许的最大转存限制 (%s)。",
				formatFileSize(media.FileSize), s.cfg.MaxFileSize)
			_, _ = s.bot.SendMessage(ctx, chatID, errMsg, "")
			return errors.New("文件体积超出系统限制")
		}
	}

	// 2. 解析并规范化安全目标路径
	destPath, err := resolveDestinationPath(s.cfg.LocalSaveDir, customPath, media.FileName)
	if err != nil {
		errMsg := fmt.Sprintf("⚠️ 目标路径非法: %v", err)
		_, _ = s.bot.SendMessage(ctx, chatID, errMsg, "")
		return err
	}

	// 确保目标父目录存在
	parentDir := filepath.Dir(destPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		errMsg := fmt.Sprintf("❌ 创建目标目录失败: %v", err)
		_, _ = s.bot.SendMessage(ctx, chatID, errMsg, "")
		return err
	}

	// 3. 磁盘剩余空间预检 (多预留 50MB 缓冲)
	if freeSpace, err := getFreeDiskSpace(parentDir); err == nil && media.FileSize > 0 {
		required := uint64(media.FileSize) + 50*1024*1024
		if freeSpace < required {
			errMsg := fmt.Sprintf("❌ 磁盘可用空间不足！当前可用: %s，所需: %s",
				formatFileSize(int64(freeSpace)), formatFileSize(int64(required)))
			_, _ = s.bot.SendMessage(ctx, chatID, errMsg, "")
			return errors.New("磁盘空间不足")
		}
	}

	// 避免文件名冲突覆盖
	destPath = avoidFileOverwrite(destPath)
	finalFileName := filepath.Base(destPath)

	humanSize := "未知大小"
	if media.FileSize > 0 {
		humanSize = formatFileSize(media.FileSize)
	}

	// 4. 发送初始进度消息
	statusText := fmt.Sprintf("📥 正在向 Telegram 请求文件信息...\n📦 `%s` (%s)\n📁 类型: %s",
		finalFileName, humanSize, media.MediaType)
	statusMsg, err := s.bot.SendMessage(ctx, chatID, statusText, "Markdown")
	if err != nil {
		return fmt.Errorf("发送初始消息失败: %w", err)
	}
	statusMsgID := statusMsg.MessageID

	// 5. 启动后台进度跟踪器：在 Local Bot API 从 Telegram 云端拉取期间实时推送下载进度
	getFileDone := make(chan struct{})
	trackCtx, cancelTrack := context.WithCancel(ctx)
	defer cancelTrack()

	searchDir := ""
	if s.cfg.LocalAPIDataDir != "" {
		tokenDir := filepath.Join(s.cfg.LocalAPIDataDir, s.cfg.BotToken)
		if _, err := os.Stat(tokenDir); err == nil {
			searchDir = tokenDir
		} else {
			searchDir = s.cfg.LocalAPIDataDir
		}
	}

	go func() {
		ticker := time.NewTicker(s.cfg.ThrottleInterval)
		defer ticker.Stop()

		var lastDownloaded int64
		var lastReportedText string
		var lastCheckTime time.Time = time.Now()
		trackStart := time.Now()

		for {
			select {
			case <-getFileDone:
				return
			case <-trackCtx.Done():
				return
			case now := <-ticker.C:
				var currentBytes int64
				var hasGrowing bool

				if searchDir != "" {
					currentBytes, _, hasGrowing = findLatestGrowingFile(searchDir, startTime)
				}

				elapsed := now.Sub(trackStart).Seconds()
				var progressText string

				if hasGrowing && currentBytes > 0 {
					total := media.FileSize
					pct := 0
					if total > 0 {
						pct = int((currentBytes * 100) / total)
						if pct > 100 {
							pct = 100
						}
					}
					bar := renderProgressBar(pct, 10)

					speedBytesPerSec := float64(0)
					intervalSec := now.Sub(lastCheckTime).Seconds()
					if intervalSec > 0.5 && currentBytes >= lastDownloaded {
						speedBytesPerSec = float64(currentBytes-lastDownloaded) / intervalSec
					}
					lastDownloaded = currentBytes
					lastCheckTime = now

					speedStr := "计算中"
					etaStr := "计算中"
					if speedBytesPerSec > 1024 {
						speedStr = fmt.Sprintf("%s/s", formatFileSize(int64(speedBytesPerSec)))
						if total > currentBytes {
							remSec := int(float64(total-currentBytes) / speedBytesPerSec)
							etaStr = fmt.Sprintf("%ds", remSec)
						}
					}

					totalStr := humanSize
					if total > 0 {
						totalStr = formatFileSize(total)
					}

					progressText = fmt.Sprintf(
						"📥 **从 Telegram 下载中**: `%s` %d%%\n"+
							"📦 **文件**: `%s`\n"+
							"📊 **已接收**: `%s / %s`\n"+
							"⚡ **实时速度**: `%s`\n"+
							"⏳ **预计剩余**: `%s`",
						bar, pct, finalFileName, formatFileSize(currentBytes), totalStr, speedStr, etaStr,
					)
				} else {
					progressText = fmt.Sprintf(
						"📥 **从 Telegram 下载中 (Local API)**...\n"+
							"📦 **文件**: `%s` (%s)\n"+
							"⏱️ **已耗时**: `%.0fs` (云端拉取中，请稍候)",
						finalFileName, humanSize, elapsed,
					)
				}

				if progressText != lastReportedText {
					_, _ = s.bot.EditMessageText(ctx, chatID, statusMsgID, progressText, "Markdown")
					lastReportedText = progressText
				}
			}
		}
	}()

	// 6. 调用 Telegram API 获取 file_path
	tgFile, err := s.bot.GetFile(ctx, media.FileID)
	close(getFileDone)

	if err != nil {
		errMsg := fmt.Sprintf("❌ 获取 Telegram 文件元信息失败: %v", err)
		_, _ = s.bot.EditMessageText(ctx, chatID, statusMsgID, errMsg, "")
		return err
	}

	if tgFile.FileSize > 0 && media.FileSize <= 0 {
		media.FileSize = tgFile.FileSize
		humanSize = formatFileSize(media.FileSize)
	}

	// 7. 尝试本地零拷贝 / 硬链接优化
	// 在 Local Bot API 模式下，如果 Bot 和 Local API 在同一机器或挂载了数据卷，文件可能已在本地磁盘
	if s.tryLocalDirectLinkOrCopy(tgFile.FilePath, destPath) {
		log.Printf("⚡ 本地直通命中！成功将文件免网络转存至: %s", destPath)
		return s.notifySuccess(ctx, chatID, statusMsgID, finalFileName, humanSize, destPath, time.Since(startTime))
	}

	// 8. 回退至 Local API HTTP 127.0.0.1 流式下载
	tempFilePath := destPath + ".saving"
	outFile, err := os.OpenFile(tempFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		errMsg := fmt.Sprintf("❌ 无法在本地创建临时文件: %v", err)
		_, _ = s.bot.EditMessageText(ctx, chatID, statusMsgID, errMsg, "")
		return err
	}

	// 清理临时文件的闭环
	cleanupTemp := true
	defer func() {
		outFile.Close()
		if cleanupTemp {
			_ = os.Remove(tempFilePath)
		}
	}()

	lastUpdate := time.Now()
	var lastReportedText string
	downloadStartTime := time.Now()

	onProgress := func(downloaded, total int64) {
		if total <= 0 && media.FileSize > 0 {
			total = media.FileSize
		}

		now := time.Now()
		if now.Sub(lastUpdate) < s.cfg.ThrottleInterval {
			return
		}

		pct := 0
		if total > 0 {
			pct = int((downloaded * 100) / total)
		}
		bar := renderProgressBar(pct, 10)

		elapsed := now.Sub(downloadStartTime).Seconds()
		speedStr := "计算中"
		etaStr := "计算中"
		if elapsed > 0.5 && downloaded > 0 {
			speedBytesPerSec := float64(downloaded) / elapsed
			speedStr = fmt.Sprintf("%s/s", formatFileSize(int64(speedBytesPerSec)))
			if total > downloaded && speedBytesPerSec > 0 {
				remSec := int(float64(total-downloaded) / speedBytesPerSec)
				etaStr = fmt.Sprintf("%ds", remSec)
			}
		}

		totalStr := humanSize
		if total > 0 {
			totalStr = formatFileSize(total)
		}

		progressText := fmt.Sprintf(
			"📥 **从 Telegram 保存中**: `%s` %d%%\n"+
				"📦 **文件**: `%s`\n"+
				"📊 **已接收**: `%s / %s`\n"+
				"⚡ **传输速度**: `%s`\n"+
				"⏳ **预计剩余**: `%s`",
			bar, pct, finalFileName, formatFileSize(downloaded), totalStr, speedStr, etaStr,
		)

		if progressText != lastReportedText {
			_, _ = s.bot.EditMessageText(ctx, chatID, statusMsgID, progressText, "Markdown")
			lastUpdate = now
			lastReportedText = progressText
		}
	}

	err = s.bot.DownloadFileStream(ctx, tgFile.FilePath, outFile, onProgress)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			_, _ = s.bot.EditMessageText(context.Background(), chatID, statusMsgID, "🛑 保存任务已由用户手动取消。", "")
			return errors.New("任务已被取消")
		}
		errMsg := fmt.Sprintf("❌ 下载流中断或失败: %v", err)
		_, _ = s.bot.EditMessageText(ctx, chatID, statusMsgID, errMsg, "")
		return err
	}

	_ = outFile.Sync()
	outFile.Close()

	// 原子重命名为最终目标文件
	if err := os.Rename(tempFilePath, destPath); err != nil {
		errMsg := fmt.Sprintf("❌ 文件重命名落盘失败: %v", err)
		_, _ = s.bot.EditMessageText(ctx, chatID, statusMsgID, errMsg, "")
		return err
	}
	cleanupTemp = false
	_ = os.Chmod(destPath, 0644)

	return s.notifySuccess(ctx, chatID, statusMsgID, finalFileName, humanSize, destPath, time.Since(startTime))
}

// tryLocalDirectLinkOrCopy attempts zero-network file transfer if the file is locally available
func (s *FileSaver) tryLocalDirectLinkOrCopy(tgFilePath, destPath string) bool {
	if tgFilePath == "" {
		return false
	}

	var candidatePaths []string
	if filepath.IsAbs(tgFilePath) {
		candidatePaths = append(candidatePaths, tgFilePath)
	}

	// 若配置了 LocalAPIDataDir，尝试路径前缀替换或拼接
	if s.cfg.LocalAPIDataDir != "" {
		candidatePaths = append(candidatePaths, filepath.Join(s.cfg.LocalAPIDataDir, tgFilePath))
		// 常见 docker 映射：容器内 /var/lib/telegram-bot-api 替换为宿主机 LocalAPIDataDir
		const containerPrefix = "/var/lib/telegram-bot-api"
		if strings.HasPrefix(tgFilePath, containerPrefix) {
			rel := strings.TrimPrefix(tgFilePath, containerPrefix)
			candidatePaths = append(candidatePaths, filepath.Join(s.cfg.LocalAPIDataDir, strings.TrimPrefix(rel, "/")))
		}
	}

	for _, cand := range candidatePaths {
		info, err := os.Stat(cand)
		if err == nil && !info.IsDir() && info.Size() > 0 {
			// 首先尝试硬链接 (0 秒, 0 额外磁盘消耗)
			if err := os.Link(cand, destPath); err == nil {
				_ = os.Chmod(destPath, 0644)
				return true
			}
			// 硬链接失败（如跨磁盘分区），回退到本地快速文件复制
			if copyErr := copyLocalFile(cand, destPath); copyErr == nil {
				_ = os.Chmod(destPath, 0644)
				return true
			}
		}
	}

	return false
}

// copyLocalFile copies file contents with a fixed small buffer
func copyLocalFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	buf := make([]byte, 64*1024)
	if _, err := io.CopyBuffer(out, in, buf); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return out.Sync()
}

// notifySuccess updates Telegram message upon successful completion
func (s *FileSaver) notifySuccess(ctx context.Context, chatID int64, statusMsgID int64, fileName, humanSize, destPath string, duration time.Duration) error {
	finishText := fmt.Sprintf(
		"✅ **文件已成功保存至本地！**\n\n"+
			"📦 **文件名**: `%s`\n"+
			"💾 **大小**: `%s`\n"+
			"📂 **本地路径**: `%s`\n"+
			"⚡ **耗时**: `%.1fs`",
		fileName, humanSize, destPath, duration.Seconds(),
	)

	// 触发外部 Webhook 回调与本地 Hook 脚本
	if s.dispatcher != nil {
		var fileSize int64
		if info, err := os.Stat(destPath); err == nil {
			fileSize = info.Size()
		}
		s.dispatcher.Dispatch(&FileEventPayload{
			Event:       "file_saved",
			Source:      "telegram",
			FileName:    fileName,
			FilePath:    destPath,
			FileSize:    fileSize,
			HumanSize:   humanSize,
			MediaType:   guessMediaType(fileName),
			ChatID:      chatID,
			DurationSec: duration.Seconds(),
		})
	}

	if s.cfg.DeleteProgressMsg {
		_ = s.bot.DeleteMessage(ctx, chatID, statusMsgID)
		_, err := s.bot.SendMessage(ctx, chatID, finishText, "Markdown")
		return err
	}

	_, err := s.bot.EditMessageText(ctx, chatID, statusMsgID, finishText, "Markdown")
	return err
}
