# 🤖 AGENT.md - AI 协作与维护指南

> **致未来的 AI Agent 与维护工程师**：  
> 本文件定义了 `tgdown`（Telegram 高性能离线下载与转存 Bot）的核心设计哲学、系统架构、硬性安全约束、代码规范以及常见陷阱。在修改或扩展本项目代码前，**必须完整阅读并严格遵守以下准则**。

---

## 🎯 项目定位与核心设计哲学

`tgdown` 专为 Linux VPS（兼容从 1 Core CPU / 1GB RAM / 2GB Swap 的极端低配小鸡，到高配多核大带宽独立服务器）打造。

### 核心设计公理（绝对不可违背）：
1. **Unix 管道工哲学（Do One Thing and Do It Well）**：
   - Bot 本身只充当轻量级调度器与协议转存管道。
   - **严禁在代码中自行实现多线程下载或音视频编解码**。所有重量级 IO 与流媒体提取，必须以 CLI 子进程形式委托宿主机原生二进制工具（`aria2c`, `yt-dlp`, `curl`, `wget`）。
2. **零内存大文件传输（Zero-Memory IO via `file:///`）**：
   - 转存大文件（最高 2000MB ~ 4000MB）时，**绝不允许将文件字节流读取进 Go 进程内存**。
   - 必须通过 Telegram Local Bot API（开启 `--local` 模式）的 `file:///abs/path` 本地文件协议通知 Local API 服务完成内核级零拷贝发送。
3. **零外部 Go 依赖（Zero External Dependencies）**：
   - 保持 100% 纯 Go 标准库（`net/http`, `os/exec`, `bufio`, `syscall`, `sync`, 等）。
   - **禁止引入任何重量级三方框架**（如 GORM, Gin, Telebot, godotenv 等），以确保编译产物为极小静态单二进制（< 7MB），且常驻运行内存稳定压制在 5MB ~ 15MB。
4. **参数化优先，严禁硬编码资源紧箍咒**：
   - 任何涉及硬件资源配置的参数（并发数、分辨率限制、磁盘缓存、文件分配方式），必须收敛在 `config.go` 并支持环境变量调整。
   - **绝对不要在核心代码或 Systemd 默认配置中硬编码极端限制**（例如不可设置 `MemoryMax=30M`，否则子进程 `aria2c` / `yt-dlp` 会被内核 cgroup OOM 误杀）。

---

## 📂 代码架构与模块职责速查

```text
.
├── main.go               # 入口点：信号处理、配置加载、动态内存软限制、命令路由与 Telegram 菜单自动注册
├── config.go             # 配置中心：.env 文件自加载、环境变量读取与合法性校验、权限白名单判定
├── task_manager.go       # 并发控制器：基于容量槽位的全局并发调度器、上下文取消管理、运行态统计
├── telegram.go           # TG Local API 客户端：无依赖纯 HTTP，支持 file:/// 协议、视频流式播放、命令菜单注册与消息销毁
├── downloader.go         # aria2c 执行引擎：子进程封装、动态 ASCII 进度条渲染、3 秒节流刷新、自动落盘转存
├── ytdl.go               # yt-dlp 流媒体引擎：按需调用、防转码封装 (--remux-video mp4)、Cookie/代理注入、多行错误诊断
├── executor.go           # 诊断执行器：安全沙箱化原生 curl/wget、自动注入 -sS、剥离进度表噪声、3500 字符硬截断
├── updater.go            # 在线热升级：GitHub Releases 版本探测、原子替换、syscall.Exec 原地重载与闭环消息回执
├── status.go             # 系统探针：Linux /proc/meminfo 物理内存与 Swap 深度解析、Go 运行时指标展示
├── bot_test.go           # 单元测试集：覆盖正则、参数切片、安全防御、并发锁、状态恢复等
├── Makefile              # 构建工具：本机构建与 amd64 / arm64 交叉编译
└── .github/workflows/    # CI/CD：自动化测试、多架构构建与 Tag 触发 GitHub Releases 自动发布
```

---

## 🔒 七大硬性规范与安全铁律

当你添加新功能或重构代码时，必须遵守以下铁律：

### 1. 绝对的磁盘原子清理（Disk Protection）
每个任务必须在 `cfg.DownloadDir` 下建立唯一的子目录 `down_<task_id>`。
**必须在启动的第一时间注册 `defer os.RemoveAll(taskDir)`**。无论任务成功、失败、超时、被用户 `/cancel` 还是触发异常，该临时目录及其包含的文件和 `.aria2` 临时索引必须立即销毁。

### 2. TG API 限频与防 429 节流（Throttling）
Telegram 对 `editMessageText` 有极严格的限频（429 FloodWait）。
* 解析外部工具的 stdout/stderr 时，**必须通过 `time.Since(lastUpdate) >= cfg.ThrottleInterval` 进行节流**（默认 3 秒）。
* 严禁每一行进度都调用 TG API。
* 内容若未变更（`message is not modified`），客户端必须静默捕获，不可视为致命错误。

### 3. 子进程沙箱与命令防注入
* 所有外部进程调用必须使用 `exec.CommandContext(ctx, binName, args...)` 传递参数切片，**严禁通过 `sh -c` 拼接字符串执行**。
* 在 `executor.go` 中必须经过 `validateSafeArgs` 过滤：
  - 严禁 `file://` 协议读取本地文件；
  - 严禁 `@/path` 本地文件上传外发；
  - 严禁探测云厂商元数据接口 `169.254.169.254`；
  - 严格限制本地写盘参数（`-o`, `-O`, `-P`），对于 `wget` 若未指定输出，必须强制追加 `-O -` 导向标准输出。

### 4. 双层权限隔离模型
* **管理级命令（`/update`, `/curl`, `/wget`）**：必须且只能允许 `ADMIN_ID` 用户触发！即使在群聊白名单中，群内非管理员成员触发时必须予以拦截。
* **通用传输命令（`/down`, `/ytdl`, `/status`, `/cancel`, `/help`）**：
  - 私聊场景：仅限 `ADMIN_ID`；
  - 群聊场景：群 ID 属于 `ALLOWED_GROUP_IDS` 或发送者为 `ADMIN_ID`。

### 5. 视频媒体与文档模式智能调度
* 视频文件优先调用 `sendVideo` 并设置 `supports_streaming: true`（支持 Telegram 客户端在线点播边下边播）。
* 如果指定了 `--doc` 或发生解码兼容报错，必须优雅降级为 `sendDocument` 确保文件不丢失。

### 6. 清爽人机交互（Clean Chat UI）
* 文件成功转存并发送至 Telegram 后，若 `cfg.DeleteProgressMsg == true`，**必须自动调用 `deleteMessage` 删除过程中的进度条消息**，聊天窗口仅保留最终的文件卡片。
* 如果下载/转存**失败**，进度消息**严禁删除**，必须显示具体的错误堆栈或原因以便排错。

### 7. 自更新闭环状态传递
* 当执行 `/update` 时，新进程启动会读取 `/tmp/tg_bot_restart_state.json`，在原地将旧进程的“正在重启...”消息编辑为“🎉 服务重启成功！新版本已就绪”，严禁留下永久悬挂的中间状态。

---

## 🛠️ 常用开发、测试与发布命令

### 本地编译与测试
```bash
# 运行全部单元测试 (包含竞态检测)
go test -v -race ./...

# 静态编译极小二进制 (约 7MB)
make build

# 交叉编译 Linux AMD64 与 ARM64 双架构
make build-all
```

### 持续交付流程 (Git Tag 自动发布)
本项目配置了 GitHub Actions 自动化发布管线：
```bash
# 1. 提交代码
git add .
git commit -m "feat: your feature description"
git push origin main

# 2. 推送新版本 Tag (格式必须为 v*.*.*)
git tag -a v0.1.3 -m "Release v0.1.3: summary of changes"
git push origin v0.1.3
# GitHub Actions 将自动编译双架构产物并创建对应 Release 资产
```

---

## ⚠️ 常见运维与环境陷阱备忘

1. **Docker 容器路径映射陷阱**：
   - 宿主机 `aria2c` 下载到 `DOWNLOAD_DIR`（宿主机绝对路径）。
   - Local Bot API 容器通过 `file:///` 访问时使用的是 `CONTAINER_DOWNLOAD_DIR`（容器内路径）。
   - 修改路径转换逻辑时，必须保留 `filepath.Rel` 的自动路径映射功能。
2. **Text file busy 报错**：
   - Linux 下正在运行的二进制不可被覆盖写入（`cp` 会报错）。
   - 替换更新必须使用 `os.Rename`（对应命令行的 `mv -f`），新文件就绪后调用 `syscall.Exec` 实现原地热更。
3. **YouTube 机器人拦截风控**：
   - 机房 VPS IP 下载 YouTube 极易触发 `Sign in to confirm you're not a bot`。
   - 支持在同目录下放置 `cookies.txt`（通过 `YTDL_COOKIES_FILE` 自动注入）或配置 `YTDL_PROXY` 绕过风控。
