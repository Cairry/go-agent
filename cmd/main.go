package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go-agent/internal/agent"
	"go-agent/internal/api"
	"go-agent/internal/config"
	"go-agent/internal/llm"
	"go-agent/internal/taskruntime"
	"go-agent/internal/tools"
)

func main() {
	workspaceArg, listenArg, showHelp, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		printUsage()
		os.Exit(2)
	}
	if showHelp {
		printUsage()
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("Error: failed to load configuration: %v\n", err)
		fmt.Println("\nExpected config location: .agent/config.yaml")
		os.Exit(1)
	}

	workspaceDir := cfg.Agent.WorkspaceDir
	if workspaceArg != "" {
		workspaceDir = workspaceArg
	}
	workspaceDir, err = filepath.Abs(workspaceDir)
	if err != nil {
		fmt.Printf("Error: failed to resolve workspace path: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		fmt.Printf("Error: failed to create workspace directory: %v\n", err)
		os.Exit(1)
	}
	cfg.Agent.WorkspaceDir = workspaceDir

	listenAddr := cfg.Server.ListenAddr
	if listenArg != "" {
		listenAddr = listenArg
	}

	logDir := filepath.Join(agent.ResolveAgentDir(), "logs")
	model, err := llm.NewClient(llm.Config{
		APIKey:      cfg.LLM.APIKey,
		APIBase:     cfg.LLM.APIBase,
		Model:       cfg.LLM.Model,
		Provider:    cfg.LLM.Provider,
		MaxRetries:  cfg.LLM.Retry.MaxRetries,
		MaxTokens:   cfg.LLM.MaxTokens,
		Temperature: cfg.LLM.Temperature,
		LogDir:      logDir,
	})
	if err != nil {
		fmt.Printf("Error: failed to initialize LLM client: %v\n", err)
		os.Exit(1)
	}
	defer model.Close()

	systemPrompt := config.GetSystemPromptContent(cfg.Agent.SystemPromptPath)
	externalToolsDir := agent.ResolveExternalToolsDir()

	var skillLoader *tools.SkillLoader
	var skillTool tools.Tool
	skillsDir := cfg.Tools.SkillsDir
	if _, err := os.Stat(skillsDir); err == nil {
		tool, loader, loadErr := tools.CreateSkillTools(skillsDir)
		if loadErr == nil {
			skillLoader = loader
			skillTool = tool
		}
	}

	// The task runtime is shared by all HTTP sessions. Each chat session gets
	// its own Agent, while task/cron tools continue to use the shared runtime.
	taskRuntime := taskruntime.New(cfg, model, systemPrompt, externalToolsDir, skillLoader, skillTool)
	defer taskRuntime.Close()

	factory := func() *agent.Agent {
		options := []agent.Option{
			agent.WithMaxSteps(cfg.Agent.MaxSteps),
			agent.WithTokenLimit(cfg.Agent.TokenLimit),
			agent.WithWorkspace(cfg.Agent.WorkspaceDir),
			agent.WithSystemPrompt(systemPrompt),
		}
		if skillLoader != nil {
			options = append(options, agent.WithSkillLoader(skillLoader))
		}

		a := agent.New(model, options...)
		a.RegisterDefaultTools()
		a.LoadExternalTools(externalToolsDir)
		if skillTool != nil {
			a.RegisterTool(skillTool)
		}
		for _, tool := range taskRuntime.Tools() {
			a.RegisterTool(tool)
		}
		return a
	}

	server := api.NewServer(listenAddr, factory)
	serverErr := make(chan error, 1)
	go func() {
		fmt.Printf("HTTP API listening on http://%s\n", listenAddr)
		serverErr <- server.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serverErr:
		if err != nil {
			fmt.Printf("Error: HTTP server stopped: %v\n", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			fmt.Printf("Error: graceful shutdown failed: %v\n", err)
			os.Exit(1)
		}
	}
}

func parseFlags(args []string) (workspace, listen string, help bool, err error) {
	flags := flag.NewFlagSet("go-agent", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&listen, "listen", "", "HTTP listen address (overrides server.listen_addr)")
	flags.BoolVar(&help, "help", false, "show help")
	flags.BoolVar(&help, "h", false, "show help")
	if err := flags.Parse(args); err != nil {
		return "", "", false, err
	}
	if flags.NArg() > 1 {
		return "", "", false, fmt.Errorf("only one positional argument is supported: workspace directory")
	}
	if flags.NArg() == 1 {
		workspace = flags.Arg(0)
	}
	return workspace, listen, help, nil
}

func printUsage() {
	fmt.Println("MiniK - HTTP API Mode")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  go-agent [options] [workspace_directory]")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  -listen address  HTTP listen address (default: 127.0.0.1:8080)")
	fmt.Println("  -h, -help        Show this help")
	fmt.Println()
	fmt.Println("Endpoints:")
	fmt.Println("  GET  /healthz     Health check")
	fmt.Println("  POST /api/chat    Stream an agent response as Server-Sent Events")
	fmt.Println("  POST /api/clear   Clear a session's conversation history")
	fmt.Println("  GET  /api/stats   Get session statistics")
	fmt.Println()
	fmt.Println("Example:")
	fmt.Println(`  curl -N -X POST http://127.0.0.1:8080/api/chat \`)
	fmt.Println(`    -H 'Content-Type: application/json' \`)
	fmt.Println(`    -d '{"message":"列出当前目录的文件","session_id":"default"}'`)
}
