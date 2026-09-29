package task

import (
	"context"
	"sync"
	"time"
)

// TaskRunner is a function that executes a task description and returns a result.
// It is provided by the caller to avoid circular imports.
type TaskRunner func(ctx context.Context, description string) (result string, err error)

// TaskManager manages task lifecycle: queuing, dispatching, and execution.
type TaskManager struct {
	store     *TaskStore
	runner    TaskRunner
	timeout   time.Duration
	sem       chan struct{}  // worker pool semaphore
	wg        sync.WaitGroup // tracks running workers
	ctx       context.Context
	cancel    context.CancelFunc
	notify    chan struct{} // signals new tasks to dispatcher
	cancelsMu sync.Mutex
	cancels   map[string]context.CancelFunc // per-task cancel funcs
}

// NewTaskManager creates a new TaskManager.
// parentCtx allows the manager to respond to application-level cancellation.
func NewTaskManager(parentCtx context.Context, store *TaskStore, runner TaskRunner, maxWorkers int, timeouts ...time.Duration) *TaskManager {
	if maxWorkers <= 0 {
		maxWorkers = 3
	}
	var timeout time.Duration
	if len(timeouts) > 0 {
		timeout = timeouts[0]
	}
	ctx, cancel := context.WithCancel(parentCtx)
	return &TaskManager{
		store:   store,
		runner:  runner,
		timeout: timeout,
		sem:     make(chan struct{}, maxWorkers),
		ctx:     ctx,
		cancel:  cancel,
		notify:  make(chan struct{}, 1),
		cancels: make(map[string]context.CancelFunc),
	}
}

// Start launches the dispatcher and resumes persisted pending tasks.
func (m *TaskManager) Start() {
	m.signal()
	go m.dispatcher()
}

// Stop cancels all running tasks and waits for them to finish.
func (m *TaskManager) Stop() {
	m.cancel()
	m.wg.Wait()
}

// AddTask creates a new pending task and notifies the dispatcher.
func (m *TaskManager) AddTask(description, cronID string) (string, error) {
	t, err := m.store.Add(description, cronID)
	if err != nil {
		return "", err
	}
	m.signal()
	return t.ID, nil
}

func (m *TaskManager) signal() {
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

// CancelTask cancels a pending or running task.
func (m *TaskManager) CancelTask(id string) error {
	m.cancelsMu.Lock()
	defer m.cancelsMu.Unlock()
	if err := m.store.Cancel(id); err != nil {
		return err
	}
	if cancelFn, ok := m.cancels[id]; ok {
		cancelFn()
	}
	return nil
}

// GetTask returns a task by ID.
func (m *TaskManager) GetTask(id string) (*Task, error) {
	return m.store.Get(id)
}

// ListTasks returns tasks, optionally filtered by status.
func (m *TaskManager) ListTasks(status TaskStatus) []*Task {
	return m.store.List(status)
}

// DeleteTask deletes a single finished task by ID.
func (m *TaskManager) DeleteTask(id string) error {
	return m.store.Delete(id)
}

// DeleteFinishedTasks deletes all finished tasks and returns the count deleted.
func (m *TaskManager) DeleteFinishedTasks() (int, error) {
	return m.store.DeleteFinished()
}

// dispatcher runs in a goroutine, polling for pending tasks.
func (m *TaskManager) dispatcher() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.notify:
			m.dispatchPending()
		}
	}
}

// dispatchPending picks up all pending tasks and dispatches them to workers.
func (m *TaskManager) dispatchPending() {
	pending := m.store.ListPending()
	for _, t := range pending {
		select {
		case <-m.ctx.Done():
			return
		case m.sem <- struct{}{}: // acquire worker slot
			m.wg.Add(1)
			go m.runTask(t.ID, t.Description)
		}
	}
}

// runTask executes a single task in a worker goroutine.
func (m *TaskManager) runTask(id, description string) {
	defer func() {
		<-m.sem // release worker slot
		m.wg.Done()

		m.cancelsMu.Lock()
		delete(m.cancels, id)
		m.cancelsMu.Unlock()
	}()

	// Serialize the transition and cancellation registration with CancelTask.
	m.cancelsMu.Lock()
	if err := m.store.MarkRunning(id); err != nil {
		m.cancelsMu.Unlock()
		return // task may have been cancelled
	}

	var taskCtx context.Context
	var taskCancel context.CancelFunc
	if m.timeout > 0 {
		taskCtx, taskCancel = context.WithTimeout(m.ctx, m.timeout)
	} else {
		taskCtx, taskCancel = context.WithCancel(m.ctx)
	}
	defer taskCancel()

	m.cancels[id] = taskCancel
	m.cancelsMu.Unlock()

	result, err := m.runner(taskCtx, description)
	if taskCtx.Err() == context.DeadlineExceeded {
		_ = m.store.MarkFailed(id, taskCtx.Err())
		return
	}
	if taskCtx.Err() == context.Canceled {
		// Explicit cancellation is already persisted; shutdown is recovered on restart.
		return
	}
	if err != nil {
		_ = m.store.MarkFailed(id, err)
		return
	}

	_ = m.store.MarkCompleted(id, result)
}
