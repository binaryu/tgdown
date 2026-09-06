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

	// 运维诊断单次运行超时，防止卡死
	diagCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(diagCtx, binName, args...)

	// 使用缓冲区限制最大读取量，防止内存暴涨 (限制读取 32KB 足够完成截断判断)
	var combinedBuf bytes.Buffer
	limitedWriter := &boundedWriter{
		buf:   &combinedBuf,
		limit: 16 * 1024,
	}

	cmd.Stdout = limitedWriter
	cmd.Stderr = limitedWriter

	startTime := time.Now()
	err := cmd.Run()
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

// boundedWriter wraps a bytes.Buffer to limit maximum bytes written
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
