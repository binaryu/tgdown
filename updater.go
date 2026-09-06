package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var (
	// AppVersion is set at compile time via -ldflags="-X main.AppVersion=..."
	AppVersion = "v0.0.3"
	githubRepo = "binaryu/tgdown"
)

// GitHubRelease represents a release object from GitHub API
type GitHubRelease struct {
	TagName string        `json:"tag_name"`
	Name    string        `json:"name"`
	Body    string        `json:"body"`
	Assets  []GitHubAsset `json:"assets"`
}

// GitHubAsset represents a downloadable asset in a release
type GitHubAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// CheckAndPerformUpdate checks for latest release on GitHub, downloads and restarts
func CheckAndPerformUpdate(ctx context.Context, bot *TelegramClient, chatID int64, force bool) error {
	msg, err := bot.SendMessage(ctx, chatID, "🔍 正在检查 GitHub 最新版本...", "")
	if err != nil {
		return err
	}
	msgID := msg.MessageID

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "tgdown-updater")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		errMsg := fmt.Sprintf("❌ 获取 GitHub 版本信息失败: %v", err)
		_, _ = bot.EditMessageText(ctx, chatID, msgID, errMsg, "")
		return errors.New(errMsg)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errMsg := fmt.Sprintf("❌ GitHub API 返回异常状态码: %d", resp.StatusCode)
		_, _ = bot.EditMessageText(ctx, chatID, msgID, errMsg, "")
		return errors.New(errMsg)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		errMsg := fmt.Sprintf("❌ 解析版本信息失败: %v", err)
		_, _ = bot.EditMessageText(ctx, chatID, msgID, errMsg, "")
		return errors.New(errMsg)
	}

	latestTag := strings.TrimSpace(release.TagName)
	if latestTag == "" {
		errMsg := "❌ 未获取到有效的 Release Tag"
		_, _ = bot.EditMessageText(ctx, chatID, msgID, errMsg, "")
		return errors.New(errMsg)
	}

	if !force && latestTag == AppVersion {
		msgText := fmt.Sprintf("✅ 当前已是最新版本 (`%s`)，无需更新。\n如需重新拉取请使用: `/update force`", AppVersion)
		_, _ = bot.EditMessageText(ctx, chatID, msgID, msgText, "Markdown")
		return nil
	}

	// 匹配当前平台架构二进制 (例如: tg-transfer-bot-linux-amd64)
	targetAssetName := fmt.Sprintf("tg-transfer-bot-%s-%s", runtime.GOOS, runtime.GOARCH)
	var downloadURL string
	var assetSize int64

	for _, asset := range release.Assets {
		if asset.Name == targetAssetName {
			downloadURL = asset.BrowserDownloadURL
			assetSize = asset.Size
			break
		}
	}

	if downloadURL == "" {
		errMsg := fmt.Sprintf("❌ 在 Release %s 中未找到适配当前架构 (%s) 的构建产物", latestTag, targetAssetName)
		_, _ = bot.EditMessageText(ctx, chatID, msgID, errMsg, "")
		return errors.New(errMsg)
	}

	// 开始下载更新包
	downloadMsg := fmt.Sprintf("⬇️ 正在下载新版本 `%s` (%s)...", latestTag, formatFileSize(assetSize))
	_, _ = bot.EditMessageText(ctx, chatID, msgID, downloadMsg, "Markdown")

	execPath, err := os.Executable()
	if err != nil {
		errMsg := fmt.Sprintf("❌ 获取当前程序路径失败: %v", err)
		_, _ = bot.EditMessageText(ctx, chatID, msgID, errMsg, "")
		return errors.New(errMsg)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return err
	}

	newBinaryPath := execPath + ".new"
	defer func() {
		_ = os.Remove(newBinaryPath)
	}()

	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	dlReq.Header.Set("User-Agent", "tgdown-updater")

	dlResp, err := client.Do(dlReq)
	if err != nil {
		errMsg := fmt.Sprintf("❌ 下载二进制失败: %v", err)
		_, _ = bot.EditMessageText(ctx, chatID, msgID, errMsg, "")
		return errors.New(errMsg)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		errMsg := fmt.Sprintf("❌ 下载文件失败 (HTTP %d)", dlResp.StatusCode)
		_, _ = bot.EditMessageText(ctx, chatID, msgID, errMsg, "")
		return errors.New(errMsg)
	}

	outFile, err := os.OpenFile(newBinaryPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		errMsg := fmt.Sprintf("❌ 创建临时更新文件失败: %v", err)
		_, _ = bot.EditMessageText(ctx, chatID, msgID, errMsg, "")
		return errors.New(errMsg)
	}

	written, err := io.Copy(outFile, dlResp.Body)
	_ = outFile.Close()
	if err != nil {
		errMsg := fmt.Sprintf("❌ 写入更新文件失败: %v", err)
		_, _ = bot.EditMessageText(ctx, chatID, msgID, errMsg, "")
		return errors.New(errMsg)
	}

	if written < 1024*1024 {
		errMsg := "❌ 下载文件体积异常 (< 1MB)，更新已终止"
		_, _ = bot.EditMessageText(ctx, chatID, msgID, errMsg, "")
		return errors.New(errMsg)
	}

	// 原子替换当前运行的二进制文件
	if err := os.Rename(newBinaryPath, execPath); err != nil {
		errMsg := fmt.Sprintf("❌ 替换二进制文件失败: %v", err)
		_, _ = bot.EditMessageText(ctx, chatID, msgID, errMsg, "")
		return errors.New(errMsg)
	}

	// 保存重启状态元数据，便于新进程启动后闭环通知
	statePath := filepath.Join(os.TempDir(), "tg_bot_restart_state.json")
	stateData, _ := json.Marshal(map[string]any{
		"chat_id":    chatID,
		"message_id": msgID,
		"version":    latestTag,
	})
	_ = os.WriteFile(statePath, stateData, 0600)

	// 告知用户正在重启
	finishMsg := fmt.Sprintf("🚀 **更新成功！**\n版本: `%s` ➔ `%s`\n系统正在无缝重启生效...", AppVersion, latestTag)
	_, _ = bot.EditMessageText(ctx, chatID, msgID, finishMsg, "Markdown")

	// 留出 500ms 让 Telegram HTTP 消息发送完成
	time.Sleep(500 * time.Millisecond)

	log.Printf("🔄 触发无缝重启: %s", execPath)

	// 尝试通过 syscall.Exec 重启自身 (保持同一 PID，Systemd 与终端均完美兼容)
	err = syscall.Exec(execPath, os.Args, os.Environ())
	if err != nil {
		log.Printf("⚠️ syscall.Exec 重启失败，退出由 Systemd 重启: %v", err)
		os.Exit(0)
	}

	return nil
}

// CheckAndNotifyRestart checks if a restart state file exists, updates the status message, and cleans up
func CheckAndNotifyRestart(ctx context.Context, bot *TelegramClient) {
	statePath := filepath.Join(os.TempDir(), "tg_bot_restart_state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		return
	}
	_ = os.Remove(statePath)

	var state struct {
		ChatID    int64  `json:"chat_id"`
		MessageID int64  `json:"message_id"`
		Version   string `json:"version"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return
	}

	successText := fmt.Sprintf("🎉 **服务重启成功！**\n新版本 `%s` 已正式生效，系统运行就绪。", AppVersion)
	_, err = bot.EditMessageText(ctx, state.ChatID, state.MessageID, successText, "Markdown")
	if err != nil {
		// 如果编辑失败 (如原消息被删除或超过时限)，直接补发一条确认消息
		_, _ = bot.SendMessage(ctx, state.ChatID, successText, "Markdown")
	}
}
