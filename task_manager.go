package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// TaskInfo holds details of a running task
type TaskInfo struct {
	ID        int64
	Name      string
	ChatID    int64
	StartTime time.Time
	cancel    context.CancelFunc
}

// TaskManager coordinates task concurrency and cancellations
type TaskManager struct {
	mu          sync.Mutex
	maxTasks    int
	nextID      int64
	activeTasks map[int64]*TaskInfo
}

// NewTaskManager creates a new task manager with configurable concurrency limit
func NewTaskManager(maxTasks int) *TaskManager {
	if maxTasks <= 0 {
		maxTasks = 1
	}
	return &TaskManager{
		maxTasks:    maxTasks,
		activeTasks: make(map[int64]*TaskInfo),
	}
}

// TryAcquire attempts to acquire a task slot
func (tm *TaskManager) TryAcquire(name string, chatID int64, cancel context.CancelFunc) (bool, int64) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if len(tm.activeTasks) >= tm.maxTasks {
		return false, 0
	}

	tm.nextID++
	id := tm.nextID
	tm.activeTasks[id] = &TaskInfo{
		ID:        id,
		Name:      name,
		ChatID:    chatID,
		StartTime: time.Now(),
		cancel:    cancel,
	}
	return true, id
}

// Release frees a task slot by ID
func (tm *TaskManager) Release(id int64) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	delete(tm.activeTasks, id)
}

// CancelAllActiveTasks cancels all running tasks
func (tm *TaskManager) CancelAllActiveTasks() (int, []string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if len(tm.activeTasks) == 0 {
		return 0, nil
	}

	var names []string
	for _, task := range tm.activeTasks {
		names = append(names, task.Name)
		if task.cancel != nil {
			task.cancel()
		}
	}
	count := len(names)
	return count, names
}

// GetStatus returns the current running tasks status
func (tm *TaskManager) GetStatus() (bool, string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	count := len(tm.activeTasks)
	if count == 0 {
		return false, fmt.Sprintf("空闲 (并发槽位: 0/%d)", tm.maxTasks)
	}

	var parts []string
	for _, t := range tm.activeTasks {
		elapsed := time.Since(t.StartTime).Round(time.Second)
		parts = append(parts, fmt.Sprintf("%s (已运行 %s)", t.Name, elapsed))
	}

	desc := fmt.Sprintf("执行中 (%d/%d):\n   - %s", count, tm.maxTasks, strings.Join(parts, "\n   - "))
	return true, desc
}
