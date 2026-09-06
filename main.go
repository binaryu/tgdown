package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
)

func main() {
	// ==========================================
	// 核心架构 3：极低内存运行调优
	// 针对 1GB RAM 极低资源机器，设置 16MB 软限制与积极 GC
	// ==========================================
	debug.SetMemoryLimit(16 * 1024 * 1024) // 16 MiB GOMEMLIMIT
	debug.SetGCPercent(50)                 // 提早触发 GC 回收

	log.Println("🚀 启动 Telegram 离线下载与转存 Bot...")

	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("❌ 配置加载失败: %v", err)
	}

	bot := NewTelegramClient(cfg.BotToken, cfg.APIBase)
	taskMgr := NewTaskManager()
	downloader := NewDownloader(cfg, bot)
	executor := NewExecutor(bot)

	// 捕获系统退出信号
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("✅ 配置加载成功 | Local API: %s | 下载卷目录: %s | 超时: %s | 节流: %s",
		cfg.APIBase, cfg.DownloadDir, cfg.TaskTimeout, cfg.ThrottleInterval)
	log.Printf("🔒 白名单 Admin ID 总计: %d 个", len(cfg.AdminIDs))

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

			handleMessage(ctx, cfg, bot, taskMgr, downloader, executor, msg)
		}
	}
}

func handleMessage(
	parentCtx context.Context,
	cfg *Config,
	bot *TelegramClient,
	taskMgr *TaskManager,
	downloader *Downloader,
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
  多连接断点续传下载并转存（默认智能识别视频媒体流播放；支持 --doc 强制发送为原文件）。
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
		cancelled, taskName := taskMgr.CancelActiveTask()
		if cancelled {
			_, _ = bot.SendMessage(parentCtx, chatID, fmt.Sprintf("🛑 已成功发送取消信号，正在中止任务: `%s`", taskName), "Markdown")
		} else {
			_, _ = bot.SendMessage(parentCtx, chatID, "ℹ️ 当前没有正在运行的任务可取消。", "")
		}

	case "/update":
		force := strings.Contains(strings.ToLower(args), "force")
		taskCtx, cancel := context.WithTimeout(parentCtx, 5*time.Minute)
		taskName := "在线更新 Bot"

		if !taskMgr.TryAcquire(taskName, chatID, cancel) {
			cancel()
			_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ **系统繁忙**: 当前有任务正在运行，请等待其完成后再更新。", "Markdown")
			return
		}

		go func() {
			defer taskMgr.Release()
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

		taskCtx, cancel := context.WithTimeout(parentCtx, cfg.TaskTimeout)
		taskName := fmt.Sprintf("/down %s", params.URL)

		// 核心约束 1：单任务排队/互斥锁
		if !taskMgr.TryAcquire(taskName, chatID, cancel) {
			cancel()
			_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ **系统繁忙**: 当前已有正在运行的任务，请等待其完成或通过 /cancel 取消当前任务。", "Markdown")
			return
		}

		go func() {
			defer taskMgr.Release()
			defer cancel()

			log.Printf("▶️ 开始处理下载任务: URL=%s, 自定义名=%s, 自定义Header=%d个", params.URL, params.CustomName, len(params.Headers))
			err := downloader.DownloadAndTransfer(taskCtx, chatID, params)
			if err != nil {
				log.Printf("❌ 下载/转存任务失败: %v", err)
			} else {
				log.Printf("✅ 下载/转存任务顺利完成")
			}
		}()

	case "/curl":
		if args == "" {
			_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ 请提供 curl 参数，例如: `/curl -I https://example.com`", "Markdown")
			return
		}

		taskCtx, cancel := context.WithCancel(parentCtx)
		taskName := fmt.Sprintf("/curl %s", args)

		if !taskMgr.TryAcquire(taskName, chatID, cancel) {
			cancel()
			_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ **系统繁忙**: 当前已有任务在执行，请稍候重试。", "Markdown")
			return
		}

		go func() {
			defer taskMgr.Release()
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

		if !taskMgr.TryAcquire(taskName, chatID, cancel) {
			cancel()
			_, _ = bot.SendMessage(parentCtx, chatID, "⚠️ **系统繁忙**: 当前已有任务在执行，请稍候重试。", "Markdown")
			return
		}

		go func() {
			defer taskMgr.Release()
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
