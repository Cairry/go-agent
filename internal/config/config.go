package config

import (
	"fmt"
	"os"
	"path/filepath"

	"go-agent/internal/schema"

	"gopkg.in/yaml.v3"
)

// RetryConfig represents retry configuration
type RetryConfig struct {
	Enabled         bool    `yaml:"enabled"`
	MaxRetries      int     `yaml:"max_retries"`
	InitialDelay    float64 `yaml:"initial_delay"`
	MaxDelay        float64 `yaml:"max_delay"`
	ExponentialBase float64 `yaml:"exponential_base"`
}

// LLMConfig represents LLM configuration
type LLMConfig struct {
	APIKey      string             `yaml:"api_key"`
	APIBase     string             `yaml:"api_base"`
	Model       string             `yaml:"model"`
	Provider    schema.LLMProvider `yaml:"provider"`
	MaxTokens   int                `yaml:"max_tokens"`  // Max output tokens per request (default: provider-specific)
	Temperature float64            `yaml:"temperature"` // Sampling temperature (default: -1 = provider default)
	Retry       RetryConfig        `yaml:"retry"`
}

// AgentConfig represents agent configuration
type AgentConfig struct {
	MaxSteps         int    `yaml:"max_steps"`
	WorkspaceDir     string `yaml:"workspace_dir"`
	SystemPromptPath string `yaml:"system_prompt_path"`
	TokenLimit       int    `yaml:"token_limit"`
}

// ToolsConfig represents tools configuration
type ToolsConfig struct {
	SkillsDir     string `yaml:"skills_dir"`
	MCPConfigPath string `yaml:"mcp_config_path"`
}

// TaskConfig represents async task/subagent configuration
type TaskConfig struct {
	MaxWorkers  int `yaml:"max_workers"`  // max concurrent subagent tasks (default: 3)
	TaskTimeout int `yaml:"task_timeout"` // per-task timeout in seconds (default: 600)
}

// Config represents the main configuration
type Config struct {
	LLM   LLMConfig   `yaml:"llm"`
	Agent AgentConfig `yaml:"agent"`
	Tools ToolsConfig `yaml:"tools"`
	Task  TaskConfig  `yaml:"task"`
}

// Load loads configuration from file
func Load() (*Config, error) {
	configPath := GetDefaultConfigPath()
	if _, err := os.Stat(configPath); err != nil {
		return nil, fmt.Errorf("configuration file not found: %s", configPath)
	}

	// Read file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse YAML
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Validate and set defaults
	if err := validateAndSetDefaults(&config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &config, nil
}

// validateAndSetDefaults validates the configuration and sets default values
func validateAndSetDefaults(config *Config) error {
	// Validate API key
	if config.LLM.APIKey == "" || config.LLM.APIKey == "YOUR_API_KEY_HERE" {
		return fmt.Errorf("API key is required")
	}

	// Set LLM defaults
	if config.LLM.APIBase == "" {
		config.LLM.APIBase = "https://api.minimaxi.com/anthropic"
	}

	if config.LLM.Model == "" {
		config.LLM.Model = "MiniMax-M2"
	}

	if config.LLM.Provider == "" {
		config.LLM.Provider = schema.ProviderAnthropic
	}

	// Set LLM generation defaults
	// MaxTokens 0 means "use provider default" (handled in client code)
	// Temperature -1 means "use provider default" (since 0.0 is a valid temperature)
	if config.LLM.Temperature == 0 {
		config.LLM.Temperature = -1
	}

	// Set retry defaults
	if config.LLM.Retry.MaxRetries == 0 {
		config.LLM.Retry.MaxRetries = 3
	}

	if config.LLM.Retry.InitialDelay == 0 {
		config.LLM.Retry.InitialDelay = 1.0
	}

	if config.LLM.Retry.MaxDelay == 0 {
		config.LLM.Retry.MaxDelay = 60.0
	}

	if config.LLM.Retry.ExponentialBase == 0 {
		config.LLM.Retry.ExponentialBase = 2.0
	}

	// Set agent defaults
	if config.Agent.MaxSteps == 0 {
		config.Agent.MaxSteps = 50
	}

	if config.Agent.WorkspaceDir == "" {
		config.Agent.WorkspaceDir = "./workspace"
	}

	if config.Agent.SystemPromptPath == "" {
		config.Agent.SystemPromptPath = "system_prompt.md"
	}

	if config.Agent.TokenLimit == 0 {
		config.Agent.TokenLimit = 80000
	}

	// Set tools defaults
	if config.Tools.SkillsDir == "" {
		config.Tools.SkillsDir = filepath.Join(".agent", "skills")
	}

	if config.Tools.MCPConfigPath == "" {
		config.Tools.MCPConfigPath = "mcp.json"
	}

	// Set task defaults
	if config.Task.MaxWorkers == 0 {
		config.Task.MaxWorkers = 3
	}
	if config.Task.TaskTimeout == 0 {
		config.Task.TaskTimeout = 600
	}

	return nil
}

// GetDefaultConfigPath returns the default configuration file path
func GetDefaultConfigPath() string {
	return filepath.Join(".agent", "config.yaml")
}

func GetSystemPromptContent(path string) string {
	systemPromptPath, err := findConfigFile(path)
	if err == nil {
		data, err := os.ReadFile(systemPromptPath)
		if err == nil {
			return string(data)
		}
	}
	return "You are 小K (MiniK), a versatile AI assistant capable of executing complex tasks through a rich toolset. Your name comes from the fact that you always like to say \"OK\"."
}

// FindConfigFile searches for a configuration file in .agent/ directory
func findConfigFile(filename string) (string, error) {
	configPath := filepath.Join(".agent", filename)
	if _, err := os.Stat(configPath); err == nil {
		return configPath, nil
	}
	return "", fmt.Errorf("config file not found: %s", configPath)
}
