# ⚡ TG Transfer Bot

> 超极简 Linux VPS 打造的高性能 Telegram 离线下载与上传转存机器人。 纯vibe开发

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Memory Footprint](https://img.shields.io/badge/RAM-Under%2015MB-brightgreen.svg)]()
[![Binary Size](https://img.shields.io/badge/Binary-6.8MB-blue.svg)]()

---

## 🌟 核心设计与优势

大部分现存的 Telegram 离线下载 Bot 都存在两个致命痛点：
1. **内存泄漏 / OOM 崩溃**：尝试在 Bot 进程内用网络流读取并上传几百 MB 到 2GB 的大文件，在 1GB RAM 的小机器上频繁被 Linux OOM Killer 强杀。
2. **RPC 冗余常驻**：后台 24 小时开着 `aria2c --enable-rpc` 守护进程，空载也要白白浪费系统常驻内存。

本项目彻底推翻上述模式，专为极简机器重构：

* 🚀 **零内存开销转存（Zero-Memory IO）**：
  文件下载完成后，Bot 直接向 Telegram Local API 传递 `file:///...` 本地协议路径。由 Local API 进程直接在内核层向 Telegram 传输数据，**Bot 进程本身 0 字节读取大文件**。
* 🍃 **超低内存常驻（< 15MB RSS）**：
  零第三方重度依赖，纯 Go 标准库编写。内置 `debug.SetMemoryLimit(16MB)` 和积极 GC 回收。
* ⚡ **CLI 按需子进程（无需 RPC / 零常驻）**：
  有下载任务时按需唤起 `aria2c`（自动应用 16 分片、断点续传），任务完成后 aria2 进程彻底销毁，**100% 归还 CPU 与内存**。
* 📊 **可视化动态进度条 + 限频节流**：
  流式解析 aria2c 实时速度与 ETA，内置 3 秒节流刷新机制，杜绝 Telegram 429 FloodWait。
* 🔒 **全局单任务互斥锁**：
  针对 1GB 小机器严格限制单任务串行，避免并发打爆磁盘 IO 与带宽。支持随时 `/cancel` 撤销任务。
* 🛡️ **严格白名单防盗刷**：
  仅允许指定的 `ADMIN_ID` 管理员触发操作，未经授权的请求直接静默丢弃。
* 🧹 **原子隔离与自清理**：
  每个任务独立目录，无论传输成功、报错、超时或手动撤销，在 `defer` 中强制清空临时文件与 `.aria2` 碎片。

---

## 💬 交互指令

| 指令 | 说明 | 示例 |
| :--- | :--- | :--- |
| `/down <URL> [重命名] [--doc/--video] [其他参数]` | 唤起 aria2c 下载并秒级转存（若为 YouTube/B站等链接自动智能调度 yt-dlp） | `/down https://example.com/movie.mp4 --doc` |
| `/ytdl <URL> [720/1080] [--doc]` | 显式唤起宿主机 yt-dlp 提取流媒体视频（封顶 1080P，禁止 CPU 重编码） | `/ytdl https://www.youtube.com/watch?v=... 720` |
| `/curl <参数...>` | 原样透传执行系统 curl 诊断，超出 3500 字符自动截断 | `/curl -I https://cloudflare.com` |
| `/wget <参数...>` | 原样透传执行系统 wget 诊断，Markdown 等宽回显 | `/wget -q -O - https://httpbin.org/ip` |
| `/status` 或 `/ping` | 实时查看宿主机物理内存、Swap (`/proc/meminfo`) 与任务排队状态 | `/status` |
| `/update [force]` | 从 GitHub Releases 检查并自动下载最新二进制无缝热重载 | `/update` 或 `/update force` |
| `/cancel` | 强制终止当前正在执行的子进程任务并释放磁盘与锁 | `/cancel` |
| `/help` | 查看帮助文档 | `/help` |

---

## 🖥️ 交互预览

```text
📥 下载进度: [██████░░░░] 60%
📊 已完成: 1.20 GiB / 2.00 GiB
⚡ 实时速度: 18.50 MiB/s
⏳ 预计剩余: 43s
🔗 连接数: 16
```

---

## 🛠️ 快速部署

### 1. 宿主机准备依赖
```bash
# Debian / Ubuntu
sudo apt update && sudo apt install -y aria2 curl wget ca-certificates
```

### 2. 部署 Telegram Local Bot API (Docker 推荐)
> 拥有官方 Local API Server 是突破 Telegram 50MB 上传限制（放宽至 2000MB）并实现本地 `file:///` 秒级上传的前提。

创建 `docker-compose.yml`（或参考仓库内 `docker-compose.example.yml`）：
```yaml
version: '3.8'

services:
  telegram-bot-api:
    image: aiogram/telegram-bot-api:latest
    container_name: telegram-bot-api
    restart: always
    environment:
      TELEGRAM_API_ID: "YOUR_TELEGRAM_API_ID"       # 在 my.telegram.org 免费申请
      TELEGRAM_API_HASH: "YOUR_TELEGRAM_API_HASH"
      TELEGRAM_LOCAL: "true"                        # 必须开启
    volumes:
      - ./data:/var/lib/telegram-bot-api
      - ./temp:/tmp/telegram-bot-api                # 共享缓存目录
    ports:
      - "127.0.0.1:8081:8081"
```

启动并确保放开挂载目录读写权限：
```bash
docker compose up -d
chmod 777 ./temp
```

### 3. 配置 Bot
下载预编译好的二进制文件或从源码编译：
```bash
# 复制配置文件模板
cp .env.example .env
nano .env
```

配置说明：
```ini
# --- 必填项 ---
BOT_TOKEN=123456789:ABCdefGhIJKlmNoPQRsTUVwxyZ
ADMIN_ID=12345678
ALLOWED_GROUP_IDS=           # 允许使用 Bot 的群组 ID (逗号分隔，如 -1001234567890；留空仅限私聊管理员)
API_BASE=http://127.0.0.1:8081
DOWNLOAD_DIR=/opt/tg-bot-api/temp
CONTAINER_DOWNLOAD_DIR=/tmp/telegram-bot-api

# --- 高级可调参数 (根据机器性能自由调节) ---
MAX_CONCURRENT_TASKS=1       # 并发任务上限 (低配小鸡建议 1，高配大机可设 3 或 5)
TASK_TIMEOUT=60m             # 单任务最大执行时间
THROTTLE_INTERVAL=3s         # Telegram 进度推送刷新间隔 (建议 2.5s ~ 5s)
ARIA2_SPLIT=16               # aria2c 单任务分片连接数 (默认 16，大带宽可设 32)
ARIA2_DISK_CACHE=16M         # aria2c 磁盘写入缓存 (默认 16M，极低内存可设 4M)
ARIA2_FILE_ALLOC=falloc      # aria2c 预分配方式 (默认 falloc，低配可设 none)
MAX_FILE_SIZE=2000M          # 单文件体积上限 (默认 2000M 匹配 Local API 限制，0 为不限)
YTDL_MAX_HEIGHT=0            # yt-dlp 最高分辨率 (默认 0 不限画质；低配小鸡可设 1080 或 720)
YTDL_PROXY=                  # yt-dlp 专属代理 (如: socks5://127.0.0.1:1080，解决机房 IP 限制)
YTDL_COOKIES_FILE=cookies.txt# yt-dlp Cookies 文件路径 (防 YouTube 机器人人机验证)
BOT_MEMORY_LIMIT=            # Bot 内存软限制 (默认留空不限；1GB 机器可配置 16MiB)
```

### 4. 运行
```bash
# 赋予可执行权限
chmod +x ./tg-transfer-bot
./tg-transfer-bot
```

---

## ⚙️ Systemd 后台服务守护 (可选)

编辑 `/etc/systemd/system/tg-transfer-bot.service`：
```ini
[Unit]
Description=Telegram Transfer Bot (Low Memory High-Performance)
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/tgdown
ExecStart=/opt/tgdown/tg-transfer-bot
Restart=always
RestartSec=5s
Nice=10

[Install]
WantedBy=multi-user.target
```

启动服务：
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now tg-transfer-bot
```

---

## 📦 从源码编译

```bash
git clone https://github.com/your-username/tg-transfer-bot.git
cd tg-transfer-bot

# 编译当前平台
make build

# 交叉编译 Linux AMD64 与 ARM64
make build-all
```

---

## 🗺️ 路线图与设计哲学 (Roadmap & Philosophy)

很多离线下载工具最终走向臃肿、内存泄漏与 OOM 崩溃，正是因为无节制地堆砌功能，忽视了资源边界。本项目始终恪守 **Unix 极简哲学**：**“只做一件事，并做到极致”**。

* [x] **极低常驻内存**：纯 Go 标准库实现，常驻内存压制在 5MB ~ 10MB。
* [x] **零内存大文件传输**：Local Bot API `file:///` 本地秒传，严禁读取 2000MB 文件进进程内存。
* [x] **单任务排队互斥锁**：针对 1GB RAM 小鸡严格串行调度，杜绝并发打爆系统磁盘与网络。
* [x] **安全沙箱化诊断**：系统级 `/curl` 与 `/wget` 过滤，阻断本地写盘、文件窃取与云元数据 SSRF。
* [x] **流式媒体支持**：视频智能识别流式播放与 `--doc` 原文件无损转存自由切换。
* [x] **纯 CLI 调用 `yt-dlp` 流媒体提取**：按需调用宿主机 `yt-dlp`，零内置臃肿依赖；严格限制 1080P、无重编码混流与 1950M 熔断，保护 1GB 内存小鸡。
* [x] **一键在线自更新**：通过 `/update` 自动拉取 GitHub Releases 预编译二进制并热重载。

---

## 📄 开源许可证

本项目采用 [MIT License](LICENSE) 开源协议。
