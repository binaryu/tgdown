package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// SysMemInfo holds parsed Linux /proc/meminfo metrics
type SysMemInfo struct {
	MemTotalKB     uint64
	MemFreeKB      uint64
	MemAvailableKB uint64
	BuffersKB      uint64
	CachedKB       uint64
	SwapTotalKB    uint64
	SwapFreeKB     uint64
}

// ReadProcMemInfo parses /proc/meminfo on Linux systems
func ReadProcMemInfo() (*SysMemInfo, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info := &SysMemInfo{}
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		valFields := strings.Fields(parts[1])
		if len(valFields) == 0 {
			continue
		}

		valKB, err := strconv.ParseUint(valFields[0], 10, 64)
		if err != nil {
			continue
		}

		switch key {
		case "MemTotal":
			info.MemTotalKB = valKB
		case "MemFree":
			info.MemFreeKB = valKB
		case "MemAvailable":
			info.MemAvailableKB = valKB
		case "Buffers":
			info.BuffersKB = valKB
		case "Cached":
			info.CachedKB = valKB
		case "SwapTotal":
			info.SwapTotalKB = valKB
		case "SwapFree":
			info.SwapFreeKB = valKB
		}
	}

	return info, scanner.Err()
}

// FormatSystemStatus formats memory and task status into a Telegram Markdown message
func FormatSystemStatus(tm *TaskManager) string {
	var sb strings.Builder
	sb.WriteString("🖥 **系统与服务运行状态**\n\n")

	// 1. 系统内存与 Swap 信息 (/proc/meminfo)
	mem, err := ReadProcMemInfo()
	if err != nil {
		sb.WriteString(fmt.Sprintf("⚠️ 无法读取 /proc/meminfo: %v\n\n", err))
	} else {
		memTotalMB := float64(mem.MemTotalKB) / 1024.0
		var memUsedMB float64
		if mem.MemAvailableKB > 0 && mem.MemTotalKB >= mem.MemAvailableKB {
			memUsedMB = float64(mem.MemTotalKB-mem.MemAvailableKB) / 1024.0
		} else {
			usedKB := mem.MemTotalKB - mem.MemFreeKB - mem.BuffersKB - mem.CachedKB
			memUsedMB = float64(usedKB) / 1024.0
		}
		memPct := 0.0
		if memTotalMB > 0 {
			memPct = (memUsedMB / memTotalMB) * 100.0
		}

		swapTotalMB := float64(mem.SwapTotalKB) / 1024.0
		swapUsedMB := float64(mem.SwapTotalKB-mem.SwapFreeKB) / 1024.0
		swapPct := 0.0
		if swapTotalMB > 0 {
			swapPct = (swapUsedMB / swapTotalMB) * 100.0
		}

		sb.WriteString(fmt.Sprintf("🧠 **宿主物理内存 (RAM)**:\n   `%.1f MB / %.1f MB (%.1f%%)`\n", memUsedMB, memTotalMB, memPct))
		sb.WriteString(fmt.Sprintf("💽 **交换空间 (Swap)**:\n   `%.1f MB / %.1f MB (%.1f%%)`\n\n", swapUsedMB, swapTotalMB, swapPct))
	}

	// 2. Go Bot 进程内存指标
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	allocMB := float64(m.Alloc) / (1024.0 * 1024.0)
	sysMB := float64(m.Sys) / (1024.0 * 1024.0)
	sb.WriteString(fmt.Sprintf("🤖 **Bot 进程状态 (版本: `%s`)**:\n   - 堆活跃内存: `%.2f MB`\n   - 系统申请内存 (Sys): `%.2f MB`\n   - GC 次数: `%d`\n   - 当前 Goroutine: `%d`\n\n",
		AppVersion, allocMB, sysMB, m.NumGC, runtime.NumGoroutine()))

	// 3. 任务队列/互斥锁状态
	_, taskDesc := tm.GetStatus()
	sb.WriteString(fmt.Sprintf("📌 **全局任务状态**:\n   `%s`\n", taskDesc))

	return sb.String()
}
