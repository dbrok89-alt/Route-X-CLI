// Package serve exposes a Route-X agent over local HTTP, so it
// can be driven from a browser, another machine on the same
// network, or through a tunnel (see share). It is deliberately
// small: three routes, loopback binding by default, and a bearer
// token that is generated fresh per run and printed once.
// Anything behind a tunnel is public, so /prompt and /sessions
// always require the token — the agent can read files and run
// commands, and an unauthenticated endpoint would hand that to
// anyone who finds the URL.
package serve

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"route-x/internal/session"
)

// Handler runs one user prompt through the agent and returns the
// reply. The serve package does not know about providers; main
// wires this to runPrompt so sandbox and approval still apply.
type Handler func(prompt string) (string, error)

// Server is a loopback HTTP server for one agent.
type Server struct {
	Addr    string // e.g. 127.0.0.1:8080
	Token   string
	Handle  Handler
	handler http.Handler
}

// NewToken generates a random 32-byte bearer token, or uses
// ROUTE_X_SERVE_TOKEN when set (never from a CLI flag, so it
// cannot leak into shell history).
func NewToken() (string, error) {
	if t := os.Getenv("ROUTE_X_SERVE_TOKEN"); t != "" {
		return t, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// New builds a Server. Addr must be a loopback address unless
// allowPublic is true; refusing non-loopback binds is the default
// because the agent can run shell commands.
func New(addr, token string, handle Handler, allowPublic bool) (*Server, error) {
	host := strings.Split(addr, ":")[0]
	if !allowPublic && host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return nil, fmt.Errorf("refusing non-loopback bind %q (use --allow-public-bind to override)", addr)
	}
	if token == "" {
		return nil, fmt.Errorf("serve token must not be empty")
	}
	s := &Server{Addr: addr, Token: token, Handle: handle}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/prompt", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			return
		}
		var body struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Prompt) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "prompt is required"})
			return
		}
		reply, err := s.Handle(body.Prompt)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"result": reply})
	}))
	mux.HandleFunc("/sessions", s.auth(func(w http.ResponseWriter, r *http.Request) {
		ids, _ := session.List()
		writeJSON(w, http.StatusOK, map[string]any{"sessions": ids})
	}))
	s.handler = mux
	return s, nil
}

// auth requires "Authorization: Bearer <token>".
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.Token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or wrong bearer token"})
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ListenAndServe blocks serving HTTP.
func (s *Server) ListenAndServe() error {
	return http.ListenAndServe(s.Addr, s.handler)
}
