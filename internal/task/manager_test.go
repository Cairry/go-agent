package task

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTaskManagerResumesPendingTasks(t *testing.T) {
	store, err := NewTaskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add("resume me", ""); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	manager := NewTaskManager(context.Background(), store, func(context.Context, string) (string, error) {
		close(started)
		return "done", nil
	}, 1)
	manager.Start()
	defer manager.Stop()

	waitForTaskStatus(t, store, "task-001", StatusCompleted)
	select {
	case <-started:
	default:
		t.Fatal("pending task was not resumed")
	}
}

func TestTaskManagerTimeoutMarksTaskFailed(t *testing.T) {
	store, err := NewTaskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewTaskManager(context.Background(), store, func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, 1, 10*time.Millisecond)
	manager.Start()
	defer manager.Stop()

	if _, err := manager.AddTask("timeout me", ""); err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, store, "task-001", StatusFailed)

	task, err := store.Get("task-001")
	if err != nil {
		t.Fatal(err)
	}
	if task.Error != context.DeadlineExceeded.Error() {
		t.Fatalf("error: got %q, want %q", task.Error, context.DeadlineExceeded)
	}
}

func TestTaskManagerCancelRunningTask(t *testing.T) {
	store, err := NewTaskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	running := make(chan struct{})
	manager := NewTaskManager(context.Background(), store, func(ctx context.Context, _ string) (string, error) {
		close(running)
		<-ctx.Done()
		return "", ctx.Err()
	}, 1)
	manager.Start()
	defer manager.Stop()

	id, err := manager.AddTask("cancel me", "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-running:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}

	if err := manager.CancelTask(id); err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, store, id, StatusCancelled)
}

func TestTaskManagerMarksRunnerError(t *testing.T) {
	store, err := NewTaskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewTaskManager(context.Background(), store, func(context.Context, string) (string, error) {
		return "", errors.New("runner failed")
	}, 1)
	manager.Start()
	defer manager.Stop()

	if _, err := manager.AddTask("fail me", ""); err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, store, "task-001", StatusFailed)

	task, err := store.Get("task-001")
	if err != nil {
		t.Fatal(err)
	}
	if task.Error != "runner failed" {
		t.Fatalf("error: got %q, want %q", task.Error, "runner failed")
	}
}

func waitForTaskStatus(t *testing.T, store *TaskStore, id string, want TaskStatus) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		task, err := store.Get(id)
		if err == nil && task.Status == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	task, _ := store.Get(id)
	if task == nil {
		t.Fatalf("task %s was not created", id)
	}
	t.Fatalf("task %s status: got %q, want %q", id, task.Status, want)
}
