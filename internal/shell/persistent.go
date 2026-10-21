package shell

import (
	"sync"
)

// PersistentShell is a singleton shell instance that maintains state across the application
type PersistentShell struct {
	*Shell
	workspace string // The initial workspace directory to reset to before each execution
}

var (
	once          sync.Once
	shellInstance *PersistentShell
)

// GetPersistentShell returns the singleton persistent shell instance
// This maintains backward compatibility with the existing API
func GetPersistentShell(cwd string) *PersistentShell {
	once.Do(func() {
		shellInstance = &PersistentShell{
			Shell: NewShell(&Options{
				WorkingDir: cwd,
				Logger:     noopLogger{}, // Keep shell command logs out of the API process output
			}),
			workspace: cwd,
		}
	})
	return shellInstance
}

// ResetToWorkspace resets the shell's working directory to the initial workspace
func (p *PersistentShell) ResetToWorkspace() {
	if p.workspace != "" {
		p.Shell.mu.Lock()
		p.Shell.cwd = p.workspace
		p.Shell.mu.Unlock()
	}
}

// loggingAdapter adapts the internal slog package to the Logger interface.
// It is retained for compatibility but intentionally does not write to stdout.
type loggingAdapter struct{}

func (l *loggingAdapter) InfoPersist(msg string, keysAndValues ...any) {
	// Disabled to keep command logs out of the API process output.
	// slog.Info(msg, keysAndValues...)
}
