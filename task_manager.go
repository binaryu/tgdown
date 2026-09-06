package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// TaskInfo holds details of the current running task
type TaskInfo struct {
	Name      string
	ChatID    int64
	StartTime time.Time
}

// TaskManager coordinates single-task concurrency and cancellations
type TaskManager struct {
	mu         sync.Mutex
	activeTask *TaskInfo
	cancelFn   context.CancelFunc
}

// NewTaskManager creates a new task manager
func NewTaskManager() *TaskManager {
	return &TaskManager{}
}

// TryAcquire attempts to acquire the exclusive task lock
func (tm *TaskManager) TryAcquire(name string, chatID int64, cancel context.CancelFunc) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.activeTask != nil {
		return false
	}

	tm.activeTask = &TaskInfo{
		Name:      name,
		ChatID:    chatID,
		StartTime: time.Now(),
	}
	tm.cancelFn = cancel
	return true
}

// Release frees the exclusive task lock
func (tm *TaskManager) Release() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.activeTask = nil
	tm.cancelFn = nil
}

// CancelActiveTask cancels the current active task if any
func (tm *TaskManager) CancelActiveTask() (bool, string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.activeTask == nil || tm.cancelFn == nil {
		return false, ""
	}

	taskName := tm.activeTask.Name
	tm.cancelFn()
	return true, taskName
}

// GetStatus returns the current running task status if any
func (tm *TaskManager) GetStatus() (bool, string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.activeTask == nil {
		return false, "空闲 (Idle)"
	}

	elapsed := time.Since(tm.activeTask.StartTime).Round(time.Second)
	return true, fmt.Sprintf("执行中: %s (已运行 %s)", tm.activeTask.Name, elapsed)
}
