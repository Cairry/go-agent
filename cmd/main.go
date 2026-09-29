package main

import (
	"fmt"
	"os"
	"path/filepath"

	"go-agent/internal/agent"
	"go-agent/internal/config"
	"go-agent/internal/llm"
	"go-agent/internal/taskruntime"
	"go-agent/internal/tools"
	"go-agent/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	// Parse command line arguments
	workspaceDir := "./workspace"
	if len(os.Args) > 1 {
		if os.Args[1] == "--help" || os.Args[1] == "-h" {
			printUsage()
			return
		}
		workspaceDir = os.Args[1]
	}

	// Convert workspace to absolute path
	absWorkspaceDir, err := filepath.Abs(workspaceDir)
	if err != nil {
		fmt.Printf("Error: failed to resolve workspace path: %v\n", err)
		os.Exit(1)
	}
	workspaceDir = absWorkspaceDir

	// Ensure workspace directory exists
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		fmt.Printf("Error: failed to create workspace directory: %v\n", err)
		os.Exit(1)
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("Error: failed to load configuration: %v\n", err)
		fmt.Println("\nExpected config location: .agent/config.yaml")
		os.Exit(1)
	}

	// Override workspace directory if provided
	if workspaceDir != "" {
		cfg.Agent.WorkspaceDir = workspaceDir
	}

	// Initialize LLM client
	logDir := filepath.Join(agent.ResolveAgentDir(), "logs")
	llmConfig := llm.Config{
		APIKey:      cfg.LLM.APIKey,
		APIBase:     cfg.LLM.APIBase,
		Model:       cfg.LLM.Model,
		Provider:    cfg.LLM.Provider,
		MaxRetries:  cfg.LLM.Retry.MaxRetries,
		MaxTokens:   cfg.LLM.MaxTokens,
		Temperature: cfg.LLM.Temperature,
		LogDir:      logDir,
	}

	model, err := llm.NewClient(llmConfig)
	if err != nil {
		fmt.Printf("Error: failed to initialize LLM client: %v\n", err)
		os.Exit(1)
	}
	defer model.Close()

	// Load system prompt if available
	systemPrompt := config.GetSystemPromptContent(cfg.Agent.SystemPromptPath)

	// Resolve .agent/tools/ directory (exe dir first, then CWD fallback)
	externalToolsDir := agent.ResolveExternalToolsDir()

	// Load skills if skills directory exists
	var skillLoader *tools.SkillLoader
	var skillTool tools.Tool
	skillsDir := filepath.Join(".agent", "skills")
	if _, err := os.Stat(skillsDir); err == nil {
		tool, loader, err := tools.CreateSkillTools(skillsDir)
		if err != nil {
			// Silent error handling
		} else {
			skillLoader = loader
			skillTool = tool
		}
	}

	// Create TUI Model with all components for proper initialization
	// (this internally creates the Agent with default tools, external tools, and skill tool)
	m := tui.NewModelWithComponents(cfg, model, systemPrompt, skillLoader, skillTool)

	// Initialize background task and cron services outside the TUI package.
	taskRuntime := taskruntime.New(cfg, model, systemPrompt, externalToolsDir, skillLoader, skillTool)
	for _, message := range taskRuntime.StartupMessages() {
		m.AddStartupMessage(message)
	}
	m.SetTaskTools(taskRuntime.Tools())

	// Start Bubbletea program
	p := tea.NewProgram(
		&m,
		tea.WithAltScreen(), // Use alternate screen buffer
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	// Clean shutdown
	m.Close()
	taskRuntime.Close()
}

func printUsage() {
	fmt.Println("MiniK - Golang Version (TUI Mode)")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  go-agent [workspace_directory]")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  go-agent                    # Use ./workspace as workspace")
	fmt.Println("  go-agent /path/to/dir       # Use specific workspace directory")
	fmt.Println()
	fmt.Println("Interactive Commands:")
	fmt.Println("  /help     - Show help")
	fmt.Println("  /clear    - Clear session")
	fmt.Println("  /stats    - Show statistics")
	fmt.Println("  /quit     - Exit")
	fmt.Println()
	fmt.Println("Keyboard Shortcuts:")
	fmt.Println("  Enter         - Submit message")
	fmt.Println("  Shift+Enter   - New line")
	fmt.Println("  Ctrl+C        - Quit")
	fmt.Println()
}
