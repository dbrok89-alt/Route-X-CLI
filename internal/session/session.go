// Package session persists conversations as JSON files, like agy's
// SQLite-backed /resume. Sessions live in ~/.route-x/sessions/.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// validID is the only shape a session id may have. It blocks
// path traversal in /resume <id> (e.g. "../../etc/passwd").
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Message is one turn in a session.
type Message struct {
	Role    string `json:"role"` // user | agent
	Content string `json:"content"`
	Time    string `json:"time"`
}

// Session is a saved conversation.
type Session struct {
	ID       string    `json:"id"`
	Model    string    `json:"model"`
	Mode     string    `json:"mode"`
	Started  string    `json:"started"`
	Messages []Message `json:"messages"`
}

// NewID returns a timestamp-based session id, e.g. 20261110-020600.
func NewID() string {
	return time.Now().Format("20060102-150405")
}

// Dir returns ~/.route-x/sessions, creating it on demand.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".route-x", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// Save writes a session to disk.
func Save(s *Session) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, s.ID+".json"), data, 0o600)
}

// List returns session ids, newest first.
func List() ([]string, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		ids = append(ids, e.Name()[:len(e.Name())-len(".json")])
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, nil
}

// Match is one session that contains the search text.
type Match struct {
	ID      string
	Excerpt string // first matching message, truncated
}

// Search finds sessions whose messages contain query (case
// insensitive), newest first. It returns at most 10 matches.
func Search(query string) ([]Match, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("search needs a word")
	}
	ids, err := List()
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, id := range ids {
		s, err := Load(id)
		if err != nil {
			continue
		}
		for _, m := range s.Messages {
			if strings.Contains(strings.ToLower(m.Content), strings.ToLower(query)) {
				excerpt := m.Content
				if len(excerpt) > 120 {
					excerpt = excerpt[:120] + "…"
				}
				out = append(out, Match{ID: id, Excerpt: excerpt})
				break
			}
		}
		if len(out) >= 10 {
			break
		}
	}
	return out, nil
}

// Load reads a session by id.
func Load(id string) (*Session, error) {
	if !validID.MatchString(id) {
		return nil, fmt.Errorf("invalid session id %q", id)
	}
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil, fmt.Errorf("session %s not found: %w", id, err)
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
