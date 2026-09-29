package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go-agent/internal/agent"
	"go-agent/internal/config"
	"go-agent/internal/llm"
	"go-agent/internal/storage"
	"go-agent/internal/tools"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/reflow/wordwrap"
)

// 布局常量
const (
	headerHeight       = 1
	statusHeight       = 1
	pendingHeight      = 1 // Pending messages notification bar height
	textareaHeight     = 2 // textarea内部可编辑行数
	inputBorderHeight  = 2 // RoundedBorder上下边框各1行
	inputPaddingHeight = 0 // 上下padding都是0
	inputHeightBuffer  = 1 // 为textarea内部渲染逻辑预留buffer
	footerHeight       = 1
)

// getInputHeight 计算输入框实际占用高度
func getInputHeight() int {
	return textareaHeight + inputBorderHeight + inputPaddingHeight + inputHeightBuffer
}

// getPendingHeight 计算 pending 消息区域高度
func (m *Model) getPendingHeight() int {
	m.pendingMutex.Lock()
	defer m.pendingMutex.Unlock()

	if len(m.pendingUserMessages) > 0 {
		return pendingHeight
	}
	return 0
}

// getTodoHeight 计算 todo 列表区域高度
func (m *Model) getTodoHeight() int {
	m.todosMutex.RLock()
	defer m.todosMutex.RUnlock()

	if len(m.todos) == 0 {
		return 0 // 没有 todo 时自动隐藏
	}
	// 每个 todo 一行，加上上下边框和padding
	return len(m.todos) + 3
}

// Model 表示 TUI 应用的状态
type Model struct {
	// UI 组件
	viewport viewport.Model
	textarea textarea.Model

	// 应用状态
	agent  *agent.Agent
	config *config.Config
	ctx    context.Context

	// 会话数据
	messages []Message

	// 运行状态
	isRunning   bool
	currentStep int
	statusText  string

	// UI 尺寸
	width  int
	height int

	// 其他
	ready bool
	err   error

	// Agent 初始化相关
	model            interface{} // LLM client
	systemPrompt     string
	skillLoader      interface{}
	skillTool        interface{}
	externalToolsDir string // .agent/tools/ path (relative to executable)

	// Agent 事件 channel
	agentEventCh chan tea.Msg

	// Markdown 渲染器
	mdRenderer *glamour.TermRenderer

	// Pending user messages (for runtime message injection)
	pendingUserMessages []string
	pendingMutex        sync.Mutex

	// Context cancellation for Agent execution
	cancelFunc context.CancelFunc

	// Streaming state (for real-time response updates)
	currentStreamingIndex int              // 正在流式更新的消息索引（-1表示无）
	streamingContentBuf   *strings.Builder // 当前流式内容缓冲区（指针避免复制）
	streamingThinkingBuf  *strings.Builder // 当前流式思考内容缓冲区（指针避免复制）
	lastRenderTime        time.Time        // 上次渲染时间（用于防抖）
	renderDebounce        time.Duration    // 防抖间隔（50-100ms）

	// Todo list (for task tracking)
	todos      []storage.TodoItem
	todosMutex sync.RWMutex

	// Agent event channel (for receiving events from Agent like todo updates)
	agentInternalEventCh <-chan interface{}

	// Startup messages to display when TUI becomes ready (errors, diagnostics)
	startupMessages []string

	// Terminal OSC 11 responses can arrive as ordinary key runes in chunks.
	terminalReportProbe    string
	droppingTerminalReport bool
	terminalReportMode     string
}

// NewModelWithComponents 创建一个新的 TUI Model（带所有组件用于初始化）
func NewModelWithComponents(cfg *config.Config, model interface{}, systemPrompt string, skillLoader, skillTool interface{}) Model {
	// Avoid OSC 11 background-color probes leaking into the textarea on some terminals.
	lipgloss.SetHasDarkBackground(true)

	// 创建输入区域
	ta := textarea.New()
	ta.Placeholder = "Ask MiniAgent to do anything"
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.SetHeight(textareaHeight)
	ta.ShowLineNumbers = false
	ta.Focus()

	// 禁用多行输入：Enter 键用于提交，不插入换行
	ta.KeyMap.InsertNewline.SetEnabled(false)

	// 创建会话区域（滚动视口）
	vp := viewport.New(0, 0)

	// Resolve .agent/tools/ directory (relative to executable)
	extToolsDir := agent.ResolveExternalToolsDir()

	// 创建事件 channel（用于 Agent 向 TUI 发送事件）
	eventCh := make(chan interface{}, 10)

	// 初始化 Agent
	agentOptions := []agent.Option{
		agent.WithMaxSteps(cfg.Agent.MaxSteps),
		agent.WithTokenLimit(cfg.Agent.TokenLimit),
		agent.WithWorkspace(cfg.Agent.WorkspaceDir),
		agent.WithSystemPrompt(systemPrompt),
		agent.WithEventCh(eventCh), // Send-only channel for Agent
	}

	if skillLoader != nil {
		agentOptions = append(agentOptions, agent.WithSkillLoader(skillLoader.(*tools.SkillLoader)))
	}

	a := agent.New(model.(llm.Client), agentOptions...)
	a.RegisterDefaultTools()
	a.LoadExternalTools(extToolsDir)

	if skillTool != nil {
		a.RegisterTool(skillTool.(tools.Tool))
	}

	// 创建 Markdown 渲染器（使用暗色主题，初始宽度80）
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(80),
	)

	return Model{
		agent:                 a,
		config:                cfg,
		ctx:                   context.Background(),
		textarea:              ta,
		viewport:              vp,
		messages:              []Message{},
		ready:                 false,
		model:                 model,
		systemPrompt:          systemPrompt,
		skillLoader:           skillLoader,
		skillTool:             skillTool,
		externalToolsDir:      extToolsDir,
		mdRenderer:            renderer,
		currentStreamingIndex: -1,
		streamingContentBuf:   &strings.Builder{},
		streamingThinkingBuf:  &strings.Builder{},
		renderDebounce:        75 * time.Millisecond, // 75ms防抖间隔
		agentInternalEventCh:  eventCh,               // Receive-only channel for TUI
		startupMessages: []string{
			"👋 Welcome to MiniK!",
			"Tell me what you want to do, then press Enter to start.",
		},
	}
}

// Close releases resources held by the TUI model (agent logger, etc.).
func (m *Model) Close() {
	if m.agent != nil {
		m.agent.Close()
	}
}

// SetTaskTools registers task/cron tools on the agent.
func (m *Model) SetTaskTools(tt []tools.Tool) {
	for _, t := range tt {
		m.agent.RegisterTool(t)
	}
}

// AddStartupMessage queues a message to display when the TUI becomes ready.
// Use this instead of fmt.Printf for pre-TUI messages (stdout is wiped by AltScreen).
func (m *Model) AddStartupMessage(msg string) {
	m.startupMessages = append(m.startupMessages, msg)
}

// GetToolCount returns the number of registered tools on the agent.
func (m *Model) GetToolCount() int {
	if m.agent == nil {
		return 0
	}
	return len(m.agent.GetToolNames())
}

// Init 初始化 Model（Bubbletea 必需方法）
func (m *Model) Init() tea.Cmd {
	return textarea.Blink
}

// addMessage 添加消息到会话并更新视图
func (m *Model) addMessage(msg Message) {
	msg.Timestamp = time.Now()
	m.messages = append(m.messages, msg)

	// 更新 viewport 内容
	content := m.renderMessages()
	m.viewport.SetContent(content)

	// 滚动到底部
	m.viewport.GotoBottom()

}

// addSystemMessage 添加系统消息
func (m *Model) addSystemMessage(content string) {
	m.addMessage(Message{
		Type:    MessageTypeSystem,
		Content: content,
	})
}

// renderMessages 渲染所有消息
func (m *Model) renderMessages() string {
	var lines []string

	for _, msg := range m.messages {
		lines = append(lines, m.renderMessage(msg))
		lines = append(lines, "") // 空行分隔
	}

	return strings.Join(lines, "\n")
}

// renderMessage 渲染单条消息
func (m *Model) renderMessage(msg Message) string {
	// 计算可用宽度，预留边距
	availableWidth := m.width
	if availableWidth < 40 {
		availableWidth = 40 // 最小宽度
	}

	switch msg.Type {
	case MessageTypeUser:
		prefix := "You › "
		prefixWidth := runewidth.StringWidth(prefix)
		placeholder := strings.Repeat(" ", prefixWidth)
		wrappedText := wordwrap.String(placeholder+msg.Content, availableWidth)
		lines := strings.Split(wrappedText, "\n")
		if len(lines) > 0 {
			firstLineRunes := []rune(lines[0])
			if len(firstLineRunes) >= prefixWidth {
				lines[0] = userStyle.Render(prefix) + string(firstLineRunes[prefixWidth:])
			} else {
				lines[0] = userStyle.Render(prefix)
			}
		}
		return strings.Join(lines, "\n")

	case MessageTypeAssistant:
		prefix := "🤖 Assistant › "

		// 使用 glamour 渲染 Markdown
		if m.mdRenderer != nil {
			// 更新渲染器宽度以匹配当前终端宽度
			// 减去一些边距，确保内容不会溢出
			renderWidth := availableWidth - 4
			if renderWidth < 40 {
				renderWidth = 40
			}

			// 重新创建渲染器以更新宽度设置
			renderer, err := glamour.NewTermRenderer(
				glamour.WithStandardStyle("dark"),
				glamour.WithWordWrap(renderWidth),
			)
			if err == nil {
				m.mdRenderer = renderer
			}

			// 渲染 Markdown 内容
			rendered, err := m.mdRenderer.Render(msg.Content)
			if err == nil {
				// 成功渲染，添加前缀并返回
				lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
				if len(lines) > 0 {
					// 为第一行添加前缀
					lines[0] = assistantStyle.Render(prefix) + lines[0]
					// 后续行不需要缩进，从头开始展示
				}
				return strings.Join(lines, "\n")
			}
		}

		// 如果 glamour 渲染失败，回退到原始文本换行方式
		prefixWidth := runewidth.StringWidth(prefix)
		placeholder := strings.Repeat(" ", prefixWidth)
		wrappedText := wordwrap.String(placeholder+msg.Content, availableWidth)
		lines := strings.Split(wrappedText, "\n")
		if len(lines) > 0 {
			firstLineRunes := []rune(lines[0])
			if len(firstLineRunes) >= prefixWidth {
				lines[0] = assistantStyle.Render(prefix) + string(firstLineRunes[prefixWidth:])
			} else {
				lines[0] = assistantStyle.Render(prefix)
			}
		}
		return strings.Join(lines, "\n")

	case MessageTypeThinking:
		prefix := "🧠 Thinking › "
		prefixWidth := runewidth.StringWidth(prefix)
		placeholder := strings.Repeat(" ", prefixWidth)
		wrappedText := wordwrap.String(placeholder+msg.Content, availableWidth)
		lines := strings.Split(wrappedText, "\n")
		if len(lines) > 0 {
			firstLineRunes := []rune(lines[0])
			if len(firstLineRunes) >= prefixWidth {
				lines[0] = thinkingStyle.Render(prefix) + string(firstLineRunes[prefixWidth:])
			} else {
				lines[0] = thinkingStyle.Render(prefix)
			}
		}
		return strings.Join(lines, "\n")

	case MessageTypeToolCall:
		args := fmt.Sprintf("%v", msg.ToolArgs)
		// 保留截断逻辑，避免参数过长
		if len(args) > 200 {
			args = args[:200] + "..."
		}

		// 第一行：Tool Call
		toolCallLine := fmt.Sprintf("🔧 Tool Call: %s", msg.ToolName)

		// 第二行：Arguments (带占位符处理)
		argsPrefix := "   Arguments: "
		argsPrefixWidth := runewidth.StringWidth(argsPrefix)
		argsPlaceholder := strings.Repeat(" ", argsPrefixWidth)
		wrappedArgs := wordwrap.String(argsPlaceholder+args, availableWidth)
		argsLines := strings.Split(wrappedArgs, "\n")
		if len(argsLines) > 0 {
			firstLineRunes := []rune(argsLines[0])
			if len(firstLineRunes) >= argsPrefixWidth {
				argsLines[0] = argsPrefix + string(firstLineRunes[argsPrefixWidth:])
			} else {
				argsLines[0] = argsPrefix
			}
		}

		return toolCallStyle.Render(toolCallLine) + "\n" + strings.Join(argsLines, "\n")

	case MessageTypeToolResult:
		result := msg.Content
		// 保留截断逻辑，避免工具结果过长干扰信息流
		if len(result) > 300 {
			result = result[:300] + "..."
		}

		var resultPrefix string
		var resultStyle lipgloss.Style
		if msg.Success {
			resultPrefix = fmt.Sprintf("✓ Tool Result (%s): ", msg.ToolName)
			resultStyle = toolResultStyle
		} else {
			resultPrefix = fmt.Sprintf("✗ Tool Error (%s): ", msg.ToolName)
			resultStyle = toolErrorStyle
		}

		prefixWidth := runewidth.StringWidth(resultPrefix)
		placeholder := strings.Repeat(" ", prefixWidth)
		wrappedText := wordwrap.String(placeholder+result, availableWidth)
		lines := strings.Split(wrappedText, "\n")
		if len(lines) > 0 {
			firstLineRunes := []rune(lines[0])
			if len(firstLineRunes) >= prefixWidth {
				lines[0] = resultStyle.Render(resultPrefix) + string(firstLineRunes[prefixWidth:])
			} else {
				lines[0] = resultStyle.Render(resultPrefix)
			}
		}
		return strings.Join(lines, "\n")

	case MessageTypeSystem:
		prefix := "[System] "
		wrappedText := wordwrap.String(prefix+msg.Content, availableWidth)
		lines := strings.Split(wrappedText, "\n")
		for i := 0; i < len(lines); i++ {
			lines[i] = systemStyle.Render(lines[i])
		}
		return strings.Join(lines, "\n")

	default:
		return wordwrap.String(msg.Content, availableWidth)
	}
}

// renderStatusBar 渲染状态栏
func (m *Model) renderStatusBar() string {
	text := fmt.Sprintf("⚡ %s", m.statusText)
	return statusBarStyle.Width(m.width).Render(text)
}

// renderInput 渲染输入区域
func (m *Model) renderInput() string {
	return inputStyle.Width(m.width - 2).Render(m.textarea.View())
}

// renderFooter 渲染底部帮助文本
func (m *Model) renderFooter() string {
	content := fmt.Sprintf("%s · %s", m.config.LLM.Model, m.config.Agent.WorkspaceDir)
	return footerStyle.Width(m.width).Render(content)
}

// renderPendingMessages 渲染待处理的消息提示
func (m *Model) renderPendingMessages() string {
	m.pendingMutex.Lock()
	defer m.pendingMutex.Unlock()

	if len(m.pendingUserMessages) == 0 {
		return ""
	}

	count := len(m.pendingUserMessages)
	text := fmt.Sprintf("⏳ Queued: %d message(s) waiting to be processed", count)

	// 使用黄色背景提示
	style := lipgloss.NewStyle().
		Background(lipgloss.Color("220")).
		Foreground(lipgloss.Color("0")).
		Padding(0, 1)

	return style.Width(m.width).Render(text)
}

// View 渲染整个界面（Bubbletea 必需方法）
func (m *Model) View() string {
	if !m.ready {
		return "Initializing..."
	}

	var sections []string

	// 1. 标题
	// sections = append(sections, m.renderHeader())

	// 2. 会话区域
	sections = append(sections, m.viewport.View())

	// 3. 状态栏（如果正在运行）
	if m.isRunning {
		sections = append(sections, m.renderStatusBar())
	}

	// 4. Todo 列表（如果有 todos）
	todoView := m.renderTodoList()
	if todoView != "" {
		sections = append(sections, todoView)
	}

	// 5. Pending 消息提示（如果有待处理消息）
	pendingMsg := m.renderPendingMessages()
	if pendingMsg != "" {
		sections = append(sections, pendingMsg)
	}

	// 6. 输入区域
	sections = append(sections, m.renderInput())

	// 7. 底部
	sections = append(sections, m.renderFooter())

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

// updateLastMessage 更新最后一条消息的内容（不带防抖）
func (m *Model) updateLastMessage(newContent string) {
	if len(m.messages) == 0 {
		return
	}

	lastIdx := len(m.messages) - 1
	m.messages[lastIdx].Content = newContent

	// 重新渲染并更新viewport
	content := m.renderMessages()
	m.viewport.SetContent(content)
	m.viewport.GotoBottom()

}

// updateLastMessageWithDebounce 更新最后一条消息的内容（带防抖优化）
func (m *Model) updateLastMessageWithDebounce(newContent string) {
	if len(m.messages) == 0 {
		return
	}

	now := time.Now()

	// 立即更新消息内容
	lastIdx := len(m.messages) - 1
	m.messages[lastIdx].Content = newContent

	// 但仅当防抖时间过去后才重新渲染
	if now.Sub(m.lastRenderTime) < m.renderDebounce {
		return // 跳过此次渲染
	}

	m.lastRenderTime = now
	content := m.renderMessages()
	m.viewport.SetContent(content)
	m.viewport.GotoBottom()

}

// renderTodoList 渲染 todo 列表
func (m *Model) renderTodoList() string {
	m.todosMutex.RLock()
	defer m.todosMutex.RUnlock()

	if len(m.todos) == 0 {
		return ""
	}

	// 按状态排序：completed, in_progress, pending
	sortedTodos := make([]storage.TodoItem, len(m.todos))
	copy(sortedTodos, m.todos)

	// Sort function
	for i := 0; i < len(sortedTodos)-1; i++ {
		for j := i + 1; j < len(sortedTodos); j++ {
			// Order: completed < in_progress < pending
			order := map[string]int{"completed": 0, "in_progress": 1, "pending": 2}
			if order[sortedTodos[i].Status] > order[sortedTodos[j].Status] {
				sortedTodos[i], sortedTodos[j] = sortedTodos[j], sortedTodos[i]
			}
		}
	}

	// Find first pending task index
	nextPendingIndex := -1
	for i, todo := range sortedTodos {
		if todo.Status == "pending" {
			nextPendingIndex = i
			break
		}
	}

	// Render each todo
	var lines []string
	for i, todo := range sortedTodos {
		var checkbox string
		var style lipgloss.Style

		switch todo.Status {
		case "completed":
			checkbox = "☒"
			style = todoCompletedStyle
		case "in_progress":
			checkbox = "☐"
			style = todoInProgressStyle
		case "pending":
			checkbox = "☐"
			// First pending task gets special highlight
			if i == nextPendingIndex {
				style = todoNextStyle
			} else {
				style = todoPendingStyle
			}
		}

		line := fmt.Sprintf("%s %s", checkbox, todo.Content)
		lines = append(lines, style.Render(line))
	}

	// Join lines and wrap in box
	content := strings.Join(lines, "\n")
	return todoBoxStyle.Width(m.width - 2).Render(content)
}
