package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const maxOutputChars = 3500

// Executor handles external diagnostic tool executions (curl, wget)
type Executor struct {
	bot *TelegramClient
}

// NewExecutor creates a new diagnostic executor
func NewExecutor(bot *TelegramClient) *Executor {
	return &Executor{bot: bot}
}

// RunCommand executes a command (curl or wget) with bounded output and context timeout
func (e *Executor) RunCommand(ctx context.Context, chatID int64, binName string, rawArgs string) {
	args := parseArgs(rawArgs)
	if len(args) == 0 {
		_, _ = e.bot.SendMessage(ctx, chatID, fmt.Sprintf("⚠️ 请提供 %s 命令参数，例如: `/%s -I https://example.com`", binName, binName), "Markdown")
		return
	}

	if _, err := exec.LookPath(binName); err != nil {
		_, _ = e.bot.SendMessage(ctx, chatID, fmt.Sprintf("❌ 宿主机未安装 %s 命令，请先安装: `apt install -y %s`", binName, binName), "Markdown")
		return
	}

	// 核心安全防护：严格校验诊断参数，阻断落盘、本地文件窃取与高危请求
	safeArgs, err := validateSafeArgs(binName, args)
	if err != nil {
		_, _ = e.bot.SendMessage(ctx, chatID, fmt.Sprintf("🛡️ **安全策略拦截**: %v", err), "Markdown")
		return
	}

	// 运维诊断单次运行超时，防止卡死
	diagCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(diagCtx, binName, safeArgs...)

	// 使用缓冲区限制最大读取量，防止内存暴涨 (限制读取 32KB 足够完成截断判断)
	var combinedBuf bytes.Buffer
	limitedWriter := &boundedWriter{
		buf:   &combinedBuf,
		limit: 16 * 1024,
	}

	cmd.Stdout = limitedWriter
	cmd.Stderr = limitedWriter

	startTime := time.Now()
	err = cmd.Run()
	duration := time.Since(startTime).Round(time.Millisecond)

	rawOutput := combinedBuf.String()
	outputRunes := []rune(rawOutput)
	truncated := false

	if len(outputRunes) > maxOutputChars {
		outputRunes = outputRunes[:maxOutputChars]
		truncated = true
	}

	displayOutput := string(outputRunes)
	if strings.TrimSpace(displayOutput) == "" {
		displayOutput = "(命令已执行，但无标准输出与错误输出)"
	}

	var statusHeader string
	if err != nil {
		if diagCtx.Err() == context.DeadlineExceeded {
			statusHeader = fmt.Sprintf("⏰ **%s 执行超时 (耗时: %s)**", binName, duration)
		} else {
			statusHeader = fmt.Sprintf("❌ **%s 执行报错 (%v, 耗时: %s)**", binName, err, duration)
		}
	} else {
		statusHeader = fmt.Sprintf("✅ **%s 执行成功 (耗时: %s)**", binName, duration)
	}

	var finalMsg strings.Builder
	finalMsg.WriteString(statusHeader)
	finalMsg.WriteString("\n```text\n")
	finalMsg.WriteString(displayOutput)
	finalMsg.WriteString("\n```")

	if truncated {
		finalMsg.WriteString("\n⚠️ *[输出过长，已自动截断至 3500 字符以内]*")
	}

	_, _ = e.bot.SendMessage(ctx, chatID, finalMsg.String(), "Markdown")
}

// validateSafeArgs validates and sanitizes arguments for curl and wget
func validateSafeArgs(binName string, args []string) ([]string, error) {
	var safeArgs []string
	hasWgetStdout := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		lower := strings.ToLower(arg)

		// 1. 禁止本地文件协议 (file://)
		if strings.Contains(lower, "file://") {
			return nil, fmt.Errorf("禁止使用 `file://` 协议读取宿主机文件")
		}

		// 2. 禁止云厂商元数据 IP (防凭据盗取 SSRF)
		if strings.Contains(lower, "169.254.169.254") {
			return nil, fmt.Errorf("禁止探测云厂商元数据接口 (`169.254.169.254`)")
		}

		// 3. 禁止 @ 文件引用 (例如 curl -d @/etc/passwd 或 curl -F "file=@/root/.ssh/id_rsa")
		if strings.HasPrefix(arg, "@") || strings.Contains(arg, "=@") {
			return nil, fmt.Errorf("禁止使用 `@` 读取并外发宿主机敏感文件")
		}

		if binName == "curl" {
			// 禁止落盘输出相关参数
			if arg == "-o" || arg == "-O" || lower == "--output" || lower == "--remote-name" ||
				strings.HasPrefix(lower, "--output=") || lower == "--dump-header" ||
				arg == "-c" || lower == "--cookie-jar" || lower == "--trace" || lower == "--trace-ascii" ||
				arg == "-K" || lower == "--config" || lower == "--upload-file" || arg == "-T" {
				return nil, fmt.Errorf("curl 诊断命令仅支持回显，禁止写入本地磁盘 (`%s`)", arg)
			}
			safeArgs = append(safeArgs, arg)
		} else if binName == "wget" {
			// 禁止递归爬取打满磁盘
			if arg == "-r" || lower == "--recursive" || arg == "-m" || lower == "--mirror" {
				return nil, fmt.Errorf("禁止使用 wget 递归爬取参数 (`%s`)", arg)
			}

			// 禁止写入指定日志或配置文件
			if arg == "-o" || arg == "-a" || lower == "--output-file" || lower == "--append-output" ||
				arg == "-P" || lower == "--directory-prefix" || lower == "--post-file" || lower == "--config" {
				return nil, fmt.Errorf("wget 禁止指定写入本地文件或读取本地配置 (`%s`)", arg)
			}

			// 检查 -O 参数
			if arg == "-O" || lower == "--output-document" {
				if i+1 < len(args) {
					target := args[i+1]
					if target == "-" || target == "/dev/stdout" || target == "/dev/null" {
						hasWgetStdout = true
						safeArgs = append(safeArgs, arg, target)
						i++
						continue
					}
					return nil, fmt.Errorf("wget 仅允许输出到终端标准流 (`-O -`)，禁止指定本地保存文件名 (`%s`)", target)
				}
			} else if strings.HasPrefix(lower, "--output-document=") {
				target := arg[len("--output-document="):]
				if target == "-" || target == "/dev/stdout" || target == "/dev/null" {
					hasWgetStdout = true
					safeArgs = append(safeArgs, arg)
					continue
				}
				return nil, fmt.Errorf("wget 仅允许输出到终端标准流 (`--output-document=-`)，禁止指定本地文件名")
			} else if arg == "-O-" || arg == "-qO-" {
				hasWgetStdout = true
				safeArgs = append(safeArgs, arg)
				continue
			} else {
				safeArgs = append(safeArgs, arg)
			}
		}
	}

	// 如果 wget 用户未指定 -O -，则默认追加 -O -，强制将内容输出至 stdout 而不是写入当前目录的文件
	if binName == "wget" && !hasWgetStdout {
		safeArgs = append(safeArgs, "-O", "-")
	}

	return safeArgs, nil
}
type boundedWriter struct {
	buf   *bytes.Buffer
	limit int
	wrote int
}

func (w *boundedWriter) Write(p []byte) (n int, err error) {
	if w.wrote >= w.limit {
		return len(p), nil // 丢弃多余输入，防止缓冲区膨胀
	}
	remain := w.limit - w.wrote
	if len(p) > remain {
		n, err = w.buf.Write(p[:remain])
		w.wrote += n
		return len(p), err
	}
	n, err = w.buf.Write(p)
	w.wrote += n
	return n, err
}

// parseArgs parses command-line arguments handling single and double quotes
func parseArgs(input string) []string {
	var args []string
	var current strings.Builder
	var inSingleQuote, inDoubleQuote, escaped bool

	input = strings.TrimSpace(input)
	for _, r := range input {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}

		if r == '\\' && !inSingleQuote {
			escaped = true
			continue
		}

		if r == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
			continue
		}

		if r == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
			continue
		}

		if (r == ' ' || r == '\t' || r == '\n') && !inSingleQuote && !inDoubleQuote {
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
			continue
		}

		current.WriteRune(r)
	}

	if current.Len() > 0 {
		args = append(args, current.String())
	}

	return args
}
