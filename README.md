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
| `/down <URL> [可选重命名] [额外参数]` | 唤起 aria2c 下载并秒级转存（支持 Cookie / Token / Referer） | `/down https://example.com/file.zip backup.zip -H "Authorization: Bearer xxx" --cookie "session=abc"` |
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
# Bot Token
BOT_TOKEN=123456789:ABCdefGhIJKlmNoPQRsTUVwxyZ

# 允许使用机器人的管理员 Telegram ID (支持逗号分隔多个)
ADMIN_ID=12345678

# Local Bot API 地址
API_BASE=http://127.0.0.1:8081

# 宿主机上真实的 temp 文件夹绝对路径
DOWNLOAD_DIR=/opt/tg-bot-api/temp

# 容器内部对应挂载路径 (对应 docker-compose 冒号右侧)
CONTAINER_DOWNLOAD_DIR=/tmp/telegram-bot-api

# 单个下载任务最大超时时间 (例如: 30m, 1h)
TASK_TIMEOUT=60m

# 进度刷新节流 (建议 2.5s ~ 5s，防 Telegram 429 限制)
THROTTLE_INTERVAL=3s

# aria2c 分片连接数
ARIA2_SPLIT=16
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

## 📄 开源许可证

本项目采用 [MIT License](LICENSE) 开源协议。
