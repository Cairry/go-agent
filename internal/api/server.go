// Package api provides the HTTP API for the agent service.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"go-agent/internal/agent"
)

const maxRequestBody = 1 << 20 // 1 MiB

// AgentFactory creates an isolated agent for a new session.
type AgentFactory func() *agent.Agent

// ChatRequest is the request body accepted by POST /api/chat.
type ChatRequest struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id,omitempty"`
}

// Server exposes agent sessions over HTTP.
type Server struct {
	httpServer *http.Server
	factory    AgentFactory

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	mu    sync.Mutex
	agent *agent.Agent
}

// NewServer creates an HTTP server. A blank address uses 127.0.0.1:8080.
func NewServer(addr string, factory AgentFactory) *Server {
	if strings.TrimSpace(addr) == "" {
		addr = "127.0.0.1:8080"
	}

	s := &Server{
		factory:  factory,
		sessions: make(map[string]*session),
	}
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// ListenAndServe starts serving HTTP requests.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Handler returns the HTTP handler, useful when embedding the API or testing it.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// Shutdown gracefully stops accepting requests and closes all agent sessions.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.httpServer.Shutdown(ctx)

	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for _, current := range s.sessions {
		sessions = append(sessions, current)
	}
	s.sessions = make(map[string]*session)
	s.mu.Unlock()

	for _, current := range sessions {
		current.agent.Close()
	}
	return err
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /api/chat", s.handleChat)
	mux.HandleFunc("POST /api/clear", s.handleClear)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	return withCORS(mux)
}

func (s *Server) getSession(id string) (*session, error) {
	if s.factory == nil {
		return nil, errors.New("agent factory is not configured")
	}
	if id == "" {
		id = "default"
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.sessions[id]; ok {
		return current, nil
	}

	current := &session{agent: s.factory()}
	if current.agent == nil {
		return nil, errors.New("agent factory returned nil")
	}
	s.sessions[id] = current
	return current, nil
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var request ChatRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody))
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(request.Message) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message is required"})
		return
	}

	current, err := s.getSession(request.SessionID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming is not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sessionID := request.SessionID
	if sessionID == "" {
		sessionID = "default"
	}
	writeSSE(w, flusher, "start", map[string]interface{}{
		"type":       "start",
		"session_id": sessionID,
	})

	// An Agent contains mutable conversation state. Serialize requests within a
	// session while allowing different sessions to run concurrently.
	current.mu.Lock()
	defer current.mu.Unlock()

	send := func(eventType string, data interface{}) bool {
		return writeSSE(w, flusher, eventType, data) && r.Context().Err() == nil
	}

	err = current.agent.RunWithCallback(r.Context(), request.Message, agent.AgentCallback{
		OnIntent: func(status, skillName, intent, explanation string) {
			send("intent_checking", map[string]interface{}{
				"type":    "intent_checking",
				"status":  status,
				"skill":   skillName,
				"intent":  intent,
				"content": explanation,
			})
		},
		OnStep: func(step, maxSteps int) {
			send("step", map[string]interface{}{"type": "step", "step": step, "max_steps": maxSteps})
		},
		OnContentDelta: func(delta string, isFirst bool) {
			send("content", map[string]interface{}{"type": "content", "delta": delta, "is_first": isFirst})
		},
		OnThinkingDelta: func(delta string, isFirst bool) {
			send("thinking", map[string]interface{}{"type": "thinking", "delta": delta, "is_first": isFirst})
		},
		OnToolCall: func(name string, args map[string]interface{}) {
			send("tool_call", map[string]interface{}{"type": "tool_call", "name": name, "arguments": args})
		},
		OnToolResult: func(name string, success bool, details string) {
			send("tool_result", map[string]interface{}{"type": "tool_result", "name": name, "success": success, "details": details})
		},
		OnUserMessageInjected: func(content string) {
			send("user_message", map[string]interface{}{"type": "user_message", "content": content})
		},
	})

	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		send("error", map[string]interface{}{"type": "error", "error": err.Error()})
		return
	}
	send("done", map[string]interface{}{"type": "done"})
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SessionID string `json:"session_id,omitempty"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody)).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
	}

	current, err := s.getSession(request.SessionID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	current.mu.Lock()
	current.agent.ClearHistory()
	current.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	current, err := s.getSession(sessionID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	current.mu.Lock()
	stats := current.agent.GetStats()
	current.mu.Unlock()
	writeJSON(w, http.StatusOK, stats)
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, eventType string, data interface{}) bool {
	payload, err := json.Marshal(data)
	if err != nil {
		return false
	}
	if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, payload); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write JSON response: %v", err)
	}
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
