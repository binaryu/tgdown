package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func parseMemoryBytes(memStr string) int64 {
	memStr = strings.TrimSpace(strings.ToUpper(memStr))
	if memStr == "" || memStr == "0" {
		return 0
	}
	multiplier := int64(1)
	if strings.HasSuffix(memStr, "MIB") || strings.HasSuffix(memStr, "MB") {
		multiplier = 1024 * 1024
		memStr = strings.TrimSuffix(strings.TrimSuffix(memStr, "MIB"), "MB")
	} else if strings.HasSuffix(memStr, "GIB") || strings.HasSuffix(memStr, "GB") {
		multiplier = 1024 * 1024 * 1024
		memStr = strings.TrimSuffix(strings.TrimSuffix(memStr, "GIB"), "GB")
	} else if strings.HasSuffix(memStr, "KIB") || strings.HasSuffix(memStr, "KB") {
		multiplier = 1024
		memStr = strings.TrimSuffix(strings.TrimSuffix(memStr, "KIB"), "KB")
	}
	val, err := strconv.ParseInt(strings.TrimSpace(memStr), 10, 64)
	if err != nil {
		return 0
	}
	return val * multiplier
}

func main() {
	log.Println("🚀 启动 Telegram 离线下载与转存 Bot...")

	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("❌ 配置加载失败: %v", err)
	}

	// 内存软限制：若环境变量显式配置了 BOT_MEMORY_LIMIT (如 16MiB)，则开启限制与积极 GC；否则遵循 Go 运行时默认
	if memLimit := parseMemoryBytes(cfg.BotMemoryLimit); memLimit > 0 {
		debug.SetMemoryLimit(memLimit)
		debug.SetGCPercent(50)
		log.Printf("⚙️ 已应用内存软限制: %s", cfg.BotMemoryLimit)
	}

	bot := NewTelegramClient(cfg.BotToken, cfg.APIBase)
	taskMgr := NewTaskManager(cfg.MaxConcurrentTasks)
	downloader := NewDownloader(cfg, bot)
	ytdl := NewYtDownloader(cfg, bot)
	executor := NewExecutor(bot)

	// 捕获系统退出信号
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("✅ 配置加载成功 | Local API: %s | 下载卷目录: %s | 超时: %s | 节流: %s",
		cfg.APIBase, cfg.DownloadDir, cfg.TaskTimeout, cfg.ThrottleInterval)
	log.Printf("🔒 白名单 Admin ID 总计: %d 个", len(cfg.AdminIDs))

	// 检查是否有由于 /update 触发的重启状态文件，若有则闭环编辑/回执成功通知
	go CheckAndNotifyRestart(ctx, bot)

	// Polling 循环
	var offset int64 = 0

	for {
		select {
		case <-ctx.Done():
			log.Println("🛑 接收到退出信号，正在优雅关闭...")
			return
		default:
		}

		updates, err := bot.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("⚠️ 获取 Updates 失败 (可能 Local API 正在重启): %v", err)
			time.Sleep(3 * time.Second)
			continue
		}

		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}

			if update.Message == nil || update.Message.Text == "" {
				continue
			}

			msg := update.Message
			// 核心约束 2：白名单权限校验
			if msg.From == nil || !cfg.IsAdmin(msg.From.ID) {
				log.Printf("🛡️ 拦截未授权访问: UserID=%d, ChatID=%d, Text=%q",
					msg.From.ID, msg.Chat.ID, msg.Text)
				continue
			}

			handleMessage(ctx, cfg, bot, taskMgr, downloader, ytdl, executor, msg)
		}
	}
}

func handleMessage(
	parentCtx context.Context,
	cfg *Config,
	bot *TelegramClient,
	taskMgr *TaskManager,
	downloader *Downloader,
	ytdl *YtDownloader,
	executor *Executor,
	msg *Message,
) {
	text := strings.TrimSpace(msg.Text)
	chatID := msg.Chat.ID

	// 分离命令与参数
	var cmd, args string
	parts := strings.SplitN(text, " ", 2)
	cmd = parts[0]
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}

	// 适配 @botusername 后缀 (如 /status@my_bot)
	if atIdx := strings.Index(cmd, "@"); atIdx != -1 {
		cmd = cmd[:atIdx]
	}

	switch cmd {
	case "/start", "/help":
		helpText := `🤖 **Telegram 高性能转存 Bot**

针对极低资源宿主机设计，基于 aria2c 与 Local Bot API 本地文件直传。

📌 **支持指令**:
• /down <URL> [重命名] [--doc/--video] [-H "Header"] [--cookie "Cookie"]
  多连接断点续传下载并转存（智能识别视频流与常规文件，流媒体自动调度）。
• /ytdl <URL> [720/1080] [--doc]
  唤起系统 yt-dlp 提取 YouTube/B站/Twitter 等流媒体（最高1080P，禁CPU重编码）。
• /curl <参数...>
  系统原生 curl 诊断执行，自动截断 3500 字符以内。
• /wget <参数...>
  系统原生 wget 诊断执行，Markdown 等宽回显。
• /status 或 /ping
  查看当前机器内存/Swap (/proc/meminfo) 与任务队列状态。
• /update [force]
  从 GitHub Releases 检查并自动下载最新二进制无缝热重载。
• /cancel
  强制取消当前正在执行的任务并清理磁盘。`
		_, _ = bot.SendMessage(parentCtx, chatID, helpText, "Markdown")

	case "/status", "/ping":
		statusText := FormatSystemStatus(taskMgr)
		_, _ = bot.SendMessage(parentCtx, chatID, statusText, "Markdown")

	case "/cancel":
		count, names := taskMgr.CancelAllActiveTasks()
		if count > 0 {
			_, _ = bot.SendMessage(parentCtx, chatID, fmt.Sprintf("🛑 已成功中止 %d 个正在执行的任务:\n- %s", count, strings.Join(names, "\n- ")), "")
		} else {
			_, _ = bot.SendMessage(parentCtx, chatID, "ℹ️ 当前没有正在运行的任务可取消。", "")
		}

	case "/update":
		force := strings.Contains(strings.ToLower(args), "force")
		taskCtx, cancel := context.WithTimeout(parentCtx, 5*time.Minute)
		taskName := "在线更新 Bot"

		acquired, taskID := taskMgr.TryAcquire(taskName, chatID, cancel)
		if !acquired {
			cancel()
			_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ **系统繁忙**: 当前有任务正在运行，请等待其完成后再更新。", "Markdown")
			return
		}

		go func() {
			defer taskMgr.Release(taskID)
			defer cancel()

			err := CheckAndPerformUpdate(taskCtx, bot, chatID, force)
			if err != nil {
				log.Printf("❌ 在线更新失败: %v", err)
			}
		}()

	case "/down":
		if args == "" {
			_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ 请提供下载链接，格式:\n`/down <URL> [可选重命名] [-H \"Header: val\"] [--cookie \"cookies\"]`", "Markdown")
			return
		}

		params, err := ParseDownArgs(args)
		if err != nil {
			_, _ = bot.SendMessage(parentCtx, chatID, fmt.Sprintf("⚠️ 参数解析错误: %v", err), "")
			return
		}

		// 简单协议校验
		lowerURL := strings.ToLower(params.URL)
		if !strings.HasPrefix(lowerURL, "http://") &&
			!strings.HasPrefix(lowerURL, "https://") &&
			!strings.HasPrefix(lowerURL, "ftp://") &&
			!strings.HasPrefix(lowerURL, "magnet:?") {
			_, _ = bot.SendMessage(parentCtx, chatID, "❌ 仅支持 http://, https://, ftp:// 或 magnet:? 链接", "")
			return
		}

		// 智能识别：如果是常见流媒体网站，自动路由至 yt-dlp
		if IsVideoPlatformURL(params.URL) {
			taskCtx, cancel := context.WithTimeout(parentCtx, cfg.TaskTimeout)
			taskName := fmt.Sprintf("/ytdl %s", params.URL)

			acquired, taskID := taskMgr.TryAcquire(taskName, chatID, cancel)
			if !acquired {
				cancel()
				_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ **系统繁忙**: 当前已有正在运行的任务，请等待其完成或通过 /cancel 取消当前任务。", "Markdown")
				return
			}

			go func() {
				defer taskMgr.Release(taskID)
				defer cancel()

				ytParams := &YtTaskParams{
					URL:        params.URL,
					ForceDoc:   params.SendAs == "doc",
					Resolution: cfg.YtdlMaxHeight,
				}
				_ = ytdl.DownloadAndTransfer(taskCtx, chatID, ytParams)
			}()
			return
		}

		taskCtx, cancel := context.WithTimeout(parentCtx, cfg.TaskTimeout)
		taskName := fmt.Sprintf("/down %s", params.URL)

		// 核心约束 1：任务槽位并发控制
		acquired, taskID := taskMgr.TryAcquire(taskName, chatID, cancel)
		if !acquired {
			cancel()
			_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ **系统繁忙**: 当前任务队列已满，请等待现有任务完成或通过 /cancel 取消当前任务。", "Markdown")
			return
		}

		go func() {
			defer taskMgr.Release(taskID)
			defer cancel()

			log.Printf("▶️ 开始处理下载任务: URL=%s, 自定义名=%s, 自定义Header=%d个", params.URL, params.CustomName, len(params.Headers))
			err := downloader.DownloadAndTransfer(taskCtx, chatID, params)
			if err != nil {
				log.Printf("❌ 下载/转存任务失败: %v", err)
			} else {
				log.Printf("✅ 下载/转存任务顺利完成")
			}
		}()

	case "/ytdl":
		if args == "" {
			_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ 请提供视频链接，格式: `/ytdl <URL> [720/1080] [--doc]`", "Markdown")
			return
		}

		ytParams, err := ParseYtArgs(args)
		if err != nil {
			_, _ = bot.SendMessage(parentCtx, chatID, fmt.Sprintf("⚠️ 参数解析错误: %v", err), "")
			return
		}

		taskCtx, cancel := context.WithTimeout(parentCtx, cfg.TaskTimeout)
		taskName := fmt.Sprintf("/ytdl %s", ytParams.URL)

		acquired, taskID := taskMgr.TryAcquire(taskName, chatID, cancel)
		if !acquired {
			cancel()
			_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ **系统繁忙**: 当前任务队列已满，请等待现有任务完成或通过 /cancel 取消当前任务。", "Markdown")
			return
		}

		go func() {
			defer taskMgr.Release(taskID)
			defer cancel()

			log.Printf("▶️ 开始处理流媒体提取: URL=%s, 限制分辨率=%sp, ForceDoc=%v", ytParams.URL, ytParams.Resolution, ytParams.ForceDoc)
			err := ytdl.DownloadAndTransfer(taskCtx, chatID, ytParams)
			if err != nil {
				log.Printf("❌ yt-dlp 任务失败: %v", err)
			} else {
				log.Printf("✅ yt-dlp 任务顺利完成")
			}
		}()

	case "/curl":
		if args == "" {
			_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ 请提供 curl 参数，例如: `/curl -I https://example.com`", "Markdown")
			return
		}

		taskCtx, cancel := context.WithCancel(parentCtx)
		taskName := fmt.Sprintf("/curl %s", args)

		acquired, taskID := taskMgr.TryAcquire(taskName, chatID, cancel)
		if !acquired {
			cancel()
			_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ **系统繁忙**: 当前任务队列已满，请稍候重试。", "Markdown")
			return
		}

		go func() {
			defer taskMgr.Release(taskID)
			defer cancel()

			executor.RunCommand(taskCtx, chatID, "curl", args)
		}()

	case "/wget":
		if args == "" {
			_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ 请提供 wget 参数，例如: `/wget -q -O - https://example.com`", "Markdown")
			return
		}

		taskCtx, cancel := context.WithCancel(parentCtx)
		taskName := fmt.Sprintf("/wget %s", args)

		acquired, taskID := taskMgr.TryAcquire(taskName, chatID, cancel)
		if !acquired {
			cancel()
			_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ **系统繁忙**: 当前任务队列已满，请稍候重试。", "Markdown")
			return
		}

		go func() {
			defer taskMgr.Release(taskID)
			defer cancel()

			executor.RunCommand(taskCtx, chatID, "wget", args)
		}()

	default:
		// 仅响应以 / 开头的未知命令或普通对话
		if strings.HasPrefix(cmd, "/") {
			_, _ = bot.SendMessage(parentCtx, chatID, fmt.Sprintf("❓ 未知指令 `%s`，输入 /help 查看帮助。", cmd), "Markdown")
		}
	}
}
