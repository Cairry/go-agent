package agent

import "go-agent/internal/tools"

// Option is a function that configures the Agent.
type Option func(*Agent)

// WithMaxSteps sets the maximum number of steps
func WithMaxSteps(maxSteps int) Option {
	return func(a *Agent) {
		a.maxSteps = maxSteps
	}
}

// WithTokenLimit sets the token limit for summarization
func WithTokenLimit(tokenLimit int) Option {
	return func(a *Agent) {
		a.tokenLimit = tokenLimit
	}
}

// WithWorkspace sets the workspace directory
func WithWorkspace(workspace string) Option {
	return func(a *Agent) {
		a.workspace = workspace
	}
}

// WithSystemPrompt sets the system prompt
func WithSystemPrompt(prompt string) Option {
	return func(a *Agent) {
		a.systemPrompt = prompt
	}
}

// WithSkillLoader sets the skill loader
func WithSkillLoader(loader *tools.SkillLoader) Option {
	return func(a *Agent) {
		a.skillLoader = loader
	}
}
