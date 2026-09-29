// Package taskruntime wires background tasks and cron scheduling into the app.
package taskruntime

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"go-agent/internal/agent"
	"go-agent/internal/config"
	"go-agent/internal/llm"
	"go-agent/internal/task"
	"go-agent/internal/tools"
)

// Runtime owns the application-level task and cron managers.
type Runtime struct {
	taskMgr *task.TaskManager
	cronMgr *task.CronManager
	tools   []tools.Tool
	startup []string
}

// New creates and starts the task/cron runtime.
//
// Task and cron persistence are initialized independently: a cron store failure
// still leaves the task tool available, while a task store failure disables both
// background task execution and cron scheduling.
func New(cfg *config.Config, model llm.Client, systemPrompt, externalToolsDir string, skillLoader *tools.SkillLoader, skillTool tools.Tool) *Runtime {
	r := &Runtime{}
	taskDir := filepath.Join(agent.ResolveAgentDir(), "tasks")

	taskStore, err := task.NewTaskStore(taskDir)
	if err != nil {
		r.startup = append(r.startup, fmt.Sprintf("⚠️ Task store init failed: %v", err))
		return r
	}

	if err := taskStore.RecoverStaleTasks(); err != nil {
		r.startup = append(r.startup, fmt.Sprintf("⚠️ Task recovery failed: %v", err))
	}

	runner := newTaskRunner(cfg, model, systemPrompt, externalToolsDir, skillLoader, skillTool)
	r.taskMgr = task.NewTaskManager(context.Background(), taskStore, runner, cfg.Task.MaxWorkers, time.Duration(cfg.Task.TaskTimeout)*time.Second)
	r.taskMgr.Start()
	r.tools = append(r.tools, tools.NewTaskTool(r.taskMgr))

	cronStore, err := task.NewCronStore(taskDir)
	if err != nil {
		r.startup = append(r.startup, fmt.Sprintf("⚠️ Cron store init failed: %v", err))
		return r
	}

	r.cronMgr = task.NewCronManager(context.Background(), cronStore, r.taskMgr)
	r.cronMgr.Start()
	r.tools = append(r.tools, tools.NewCronTool(cronStore))

	return r
}

// Tools returns the task and cron tools that were initialized successfully.
func (r *Runtime) Tools() []tools.Tool {
	return r.tools
}

// StartupMessages returns messages that should be shown after the TUI starts.
func (r *Runtime) StartupMessages() []string {
	return r.startup
}

// Close stops cron scheduling before waiting for task workers to finish.
func (r *Runtime) Close() {
	if r.cronMgr != nil {
		r.cronMgr.Stop()
	}
	if r.taskMgr != nil {
		r.taskMgr.Stop()
	}
}

func newTaskRunner(cfg *config.Config, model llm.Client, systemPrompt, externalToolsDir string, skillLoader *tools.SkillLoader, skillTool tools.Tool) task.TaskRunner {
	return func(ctx context.Context, description string) (string, error) {
		subagentOptions := []agent.Option{
			agent.WithMaxSteps(cfg.Agent.MaxSteps),
			agent.WithTokenLimit(cfg.Agent.TokenLimit),
			agent.WithWorkspace(cfg.Agent.WorkspaceDir),
			agent.WithSystemPrompt(systemPrompt + "\n\n[You are a subagent executing an async task. Complete the task described below and return a concise result summary.]"),
		}
		if skillLoader != nil {
			subagentOptions = append(subagentOptions, agent.WithSkillLoader(skillLoader))
		}

		sub := agent.New(model, subagentOptions...)
		defer sub.Close()
		sub.RegisterDefaultTools()
		sub.UnregisterTool("todo")
		sub.LoadExternalTools(externalToolsDir)
		if skillTool != nil {
			sub.RegisterTool(skillTool)
		}

		return sub.Run(ctx, description)
	}
}
