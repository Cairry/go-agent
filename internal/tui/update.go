package tui

import (
	"fmt"
	"strings"

	"go-agent/internal/storage"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// Update routes messages to the state transition that owns them.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m, m.updateWindowSize(msg)
	case tea.KeyMsg:
		return m.updateKey(msg)
	case agentStartMsg, agentStepMsg, agentToolCallMsg, agentToolResultMsg,
		agentCompleteMsg, agentErrorMsg, agentContentDeltaMsg,
		agentThinkingDeltaMsg, agentStreamCompleteMsg, userMessageInjectedMsg,
		todoUpdateMsg:
		return m.updateAgentEvent(msg)
	default:
		return m.updateComponents(msg)
	}
}

func (m *Model) updateWindowSize(msg tea.WindowSizeMsg) tea.Cmd {
	wasReady := m.ready
	m.width = msg.Width
	m.height = msg.Height
	if !m.ready {
		m.viewport = viewport.New(msg.Width, 0)
		m.ready = true
	}
	m.resizeViewport()
	m.textarea.SetWidth(nonNegative(msg.Width - 2))

	if wasReady {
		m.viewport.SetContent(m.renderMessages())
		return nil
	}

	for _, startupMessage := range m.startupMessages {
		m.addSystemMessage(startupMessage)
	}
	m.startupMessages = nil
	return nil
}

func (m *Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		if m.isRunning && m.cancelFunc != nil {
			m.cancelFunc()
			m.addSystemMessage("⚠️ Execution cancelled by user")
			m.isRunning = false
			m.statusText = ""
			m.pendingMutex.Lock()
			m.pendingUserMessages = nil
			m.pendingMutex.Unlock()
			m.todosMutex.Lock()
			m.todos = nil
			m.todosMutex.Unlock()
			m.resizeViewport()
			return m, nil
		}
		return m, tea.Quit
	case tea.KeyEnter:
		return m.handleSubmit()
	default:
		return m.updateComponents(msg)
	}
}

func (m *Model) updateAgentEvent(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case agentStartMsg:
		m.isRunning = true
		m.addSystemMessage("Agent started...")
		m.todosMutex.Lock()
		m.todos = nil
		m.todosMutex.Unlock()
		m.resizeViewport()

	case agentStepMsg:
		m.currentStep = msg.step
		m.statusText = fmt.Sprintf("Step %d/%d", msg.step, msg.maxSteps)
		return m, m.nextAgentEvent()

	case agentToolCallMsg:
		m.addMessage(Message{
			Type:     MessageTypeToolCall,
			ToolName: msg.toolName,
			ToolArgs: msg.args,
		})
		return m, m.nextAgentEvent()

	case agentToolResultMsg:
		m.addMessage(Message{
			Type:     MessageTypeToolResult,
			ToolName: msg.toolName,
			Success:  msg.success,
			Content:  msg.details,
		})
		return m, m.nextAgentEvent()

	case agentCompleteMsg:
		m.isRunning = false
		m.statusText = ""
		m.addSystemMessage("✅ Task completed")
		m.clearCompletedTodos()
		m.resizeViewport()

	case agentErrorMsg:
		m.isRunning = false
		m.err = msg.err
		m.addSystemMessage(fmt.Sprintf("❌ Error: %v", msg.err))
		m.todosMutex.Lock()
		m.todos = nil
		m.todosMutex.Unlock()
		m.resizeViewport()

	case agentContentDeltaMsg:
		if msg.isFirst {
			m.addMessage(Message{
				Type:    MessageTypeAssistant,
				Content: msg.delta,
			})
			m.currentStreamingIndex = len(m.messages) - 1
			m.streamingContentBuf.Reset()
			m.streamingContentBuf.WriteString(msg.delta)
		} else {
			m.streamingContentBuf.WriteString(msg.delta)
			m.updateLastMessageWithDebounce(m.streamingContentBuf.String())
		}
		return m, m.nextAgentEvent()

	case agentThinkingDeltaMsg:
		if msg.isFirst {
			m.addMessage(Message{
				Type:    MessageTypeThinking,
				Content: msg.delta,
			})
			m.streamingThinkingBuf.Reset()
			m.streamingThinkingBuf.WriteString(msg.delta)
		} else {
			m.streamingThinkingBuf.WriteString(msg.delta)
			m.updateLastMessageWithDebounce(m.streamingThinkingBuf.String())
		}
		return m, m.nextAgentEvent()

	case agentStreamCompleteMsg:
		finalContent := msg.finalContent
		if finalContent == "" {
			finalContent = m.streamingContentBuf.String()
		}
		if finalContent != "" {
			if m.currentStreamingIndex >= 0 && m.currentStreamingIndex < len(m.messages) {
				m.updateLastMessage(finalContent)
			} else {
				m.addMessage(Message{
					Type:    MessageTypeAssistant,
					Content: finalContent,
				})
			}
		}
		m.currentStreamingIndex = -1
		m.streamingContentBuf.Reset()
		m.streamingThinkingBuf.Reset()
		return m, m.nextAgentEvent()

	case userMessageInjectedMsg:
		m.pendingMutex.Lock()
		if len(m.pendingUserMessages) > 0 {
			m.pendingUserMessages = m.pendingUserMessages[1:]
		}
		m.pendingMutex.Unlock()
		return m, m.nextAgentEvent()

	case todoUpdateMsg:
		m.todosMutex.Lock()
		m.todos = make([]storage.TodoItem, 0, len(msg.todos))
		for _, todo := range msg.todos {
			if todoItem, ok := todo.(storage.TodoItem); ok {
				m.todos = append(m.todos, todoItem)
			}
		}
		m.todosMutex.Unlock()
		m.resizeViewport()
		if m.isRunning {
			return m, m.nextAgentEvent()
		}
	}

	return m, nil
}

func (m *Model) updateComponents(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	cmds = append(cmds, cmd)

	if keyMsg, ok := msg.(tea.KeyMsg); !ok || keyMsg.Type != tea.KeyRunes {
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) resizeViewport() {
	if !m.ready && m.width == 0 && m.height == 0 {
		return
	}

	statusHeightAdjust := 0
	if m.isRunning {
		statusHeightAdjust = statusHeight
	}
	height := m.height - headerHeight - getInputHeight() - footerHeight -
		statusHeightAdjust - m.getPendingHeight() - m.getTodoHeight() + 1
	m.viewport.Width = m.width
	m.viewport.Height = nonNegative(height)
}

func (m *Model) clearCompletedTodos() {
	m.todosMutex.Lock()
	defer m.todosMutex.Unlock()

	if len(m.todos) == 0 {
		return
	}
	for _, todo := range m.todos {
		if todo.Status != "completed" {
			return
		}
	}
	m.todos = nil
}

func (m *Model) nextAgentEvent() tea.Cmd {
	return waitForAgentEvent(m.agentEventCh)
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func (m *Model) handleSubmit() (tea.Model, tea.Cmd) {
	input := strings.TrimSpace(m.textarea.Value())
	if input == "" {
		return m, nil
	}

	m.addMessage(Message{
		Type:    MessageTypeUser,
		Content: input,
	})
	m.textarea.Reset()

	if isCommand(input) {
		return m.handleCommand(input)
	}

	if m.isRunning {
		m.pendingMutex.Lock()
		m.pendingUserMessages = append(m.pendingUserMessages, input)
		m.pendingMutex.Unlock()
		m.agent.AppendUserMessage(input)
		return m, nil
	}

	return m, m.runAgent(input)
}

func isCommand(input string) bool {
	if strings.HasPrefix(input, "/") {
		return true
	}

	switch input {
	case "help", "clear", "stats", "history", "prompt", "quit", "exit":
		return true
	}
	return false
}
