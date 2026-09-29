package tui

import (
	"context"

	"go-agent/internal/agent"
	"go-agent/internal/tools"

	tea "github.com/charmbracelet/bubbletea"
)

// runAgent 在后台运行 Agent 并返回初始命令
func (m *Model) runAgent(prompt string) tea.Cmd {
	// 创建可取消的 context
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelFunc = cancel

	// Each run owns its event channel so stale events cannot reach a later run.
	eventCh := make(chan tea.Msg, 16)
	m.agentEventCh = eventCh

	// 启动 goroutine 监听 Agent 内部事件 (todo updates, etc.)
	go func() {
		for {
			select {
			case <-ctx.Done():
				// Context cancelled, stop listening
				return
			case event, ok := <-m.agentInternalEventCh:
				if !ok {
					// Channel closed
					return
				}
				// Convert tool events to TUI messages.
				switch e := event.(type) {
				case tools.TodoUpdateEvent:
					todos := make([]interface{}, len(e.NewTodos))
					for i, todo := range e.NewTodos {
						todos[i] = todo
					}
					if !sendAgentMessage(ctx, eventCh, todoUpdateMsg{todos: todos}) {
						return
					}
				}
			}
		}
	}()

	// 启动 Agent 执行的 goroutine
	go func() {
		defer cancel() // Cancel context when agent completes.
		defer func() {
			m.cancelFunc = nil
		}()

		// 使用带回调的 Agent 执行（流式）
		err := m.agent.RunWithCallback(ctx, prompt, agent.AgentCallback{
			OnStep: func(step, maxSteps int) {
				sendAgentMessage(ctx, eventCh, agentStepMsg{step, maxSteps})
			},
			// 流式回调
			OnContentDelta: func(delta string, isFirst bool) {
				sendAgentMessage(ctx, eventCh, agentContentDeltaMsg{delta, isFirst})
			},
			OnThinkingDelta: func(delta string, isFirst bool) {
				sendAgentMessage(ctx, eventCh, agentThinkingDeltaMsg{delta, isFirst})
			},
			OnStreamComplete: func(finalContent string) {
				sendAgentMessage(ctx, eventCh, agentStreamCompleteMsg{finalContent})
			},
			// 工具回调
			OnToolCall: func(name string, args map[string]interface{}) {
				sendAgentMessage(ctx, eventCh, agentToolCallMsg{name, args})
			},
			OnToolResult: func(name string, success bool, details string) {
				sendAgentMessage(ctx, eventCh, agentToolResultMsg{name, success, details})
			},
			OnUserMessageInjected: func(content string) {
				sendAgentMessage(ctx, eventCh, userMessageInjectedMsg{content})
			},
		})

		if err != nil {
			// Check if error is due to context cancellation
			if err == context.Canceled {
				// User cancelled, don't send error
				return
			}
			sendAgentMessage(ctx, eventCh, agentErrorMsg{err})
		} else {
			sendAgentMessage(ctx, eventCh, agentCompleteMsg{})
		}
	}()

	// 返回一个批量命令：先发送开始消息，然后开始监听事件
	return tea.Batch(
		func() tea.Msg { return agentStartMsg{} },
		waitForAgentEvent(eventCh),
	)
}

// waitForAgentEvent 等待 Agent 执行事件
func waitForAgentEvent(eventCh <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-eventCh
	}
}

func sendAgentMessage(ctx context.Context, eventCh chan<- tea.Msg, msg tea.Msg) bool {
	select {
	case eventCh <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}
