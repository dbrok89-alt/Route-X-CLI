// Package cron schedules recurring agent tasks, Hermes-style:
// a task has a name, a 5-field cron schedule, and a prompt. The
// schedule is evaluated by route-x cron run (called by the user's
// own scheduler or by hand); Route-X itself is not a daemon, so
// it never pretends to run in the background on its own.
package cron

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Task is one scheduled agent task.
type Task struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"` // 5-field cron: min hour dom month dow
	Prompt   string `json:"prompt"`
	LastRun  string `json:"last_run,omitempty"` // RFC3339
	Created  string `json:"created"`
}

// Store is the on-disk task list.
type Store struct {
	Tasks []Task `json:"tasks"`
}

func storePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".route-x", "cron.json")
}

// Load reads the task list. Missing file means no tasks.
func Load() (*Store, error) {
	path := storePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Store{}, nil
		}
		return nil, err
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Save writes the task list.
func (s *Store) Save() error {
	path := storePath()
	if path == "" {
		return fmt.Errorf("cannot find home directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(path, data, 0o644)
}

// Add inserts or replaces a task by name after validating the
// schedule.
func (s *Store) Add(t Task) error {
	if _, err := Parse(t.Schedule); err != nil {
		return err
	}
	if t.Name == "" || t.Prompt == "" {
		return fmt.Errorf("task needs a name and a prompt")
	}
	for i, existing := range s.Tasks {
		if existing.Name == t.Name {
			t.Created = existing.Created
			s.Tasks[i] = t
			return nil
		}
	}
	t.Created = time.Now().Format(time.RFC3339)
	s.Tasks = append(s.Tasks, t)
	return nil
}

// Remove deletes a task by name.
func (s *Store) Remove(name string) bool {
	for i, t := range s.Tasks {
		if t.Name == name {
			s.Tasks = append(s.Tasks[:i], s.Tasks[i+1:]...)
			return true
		}
	}
	return false
}

// Schedule is a parsed 5-field cron expression.
type Schedule struct {
	Minute, Hour, Day, Month, Weekday field
}

type field struct {
	any   bool
	vals  map[int]bool
	steps int // 0 = no step; n = every nth value in the range
}

// Parse parses "min hour dom month dow". Each field accepts *,
// a number, a range (a-b), a list (a,b,c), a step (*/n, a-b/n).
func Parse(expr string) (Schedule, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return Schedule{}, fmt.Errorf("schedule %q must have 5 fields (min hour day month weekday)", expr)
	}
	ranges := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}
	var s Schedule
	fields := [5]*field{&s.Minute, &s.Hour, &s.Day, &s.Month, &s.Weekday}
	for i, part := range parts {
		f, err := parseField(part, ranges[i][0], ranges[i][1])
		if err != nil {
			return Schedule{}, fmt.Errorf("field %d (%q): %w", i+1, part, err)
		}
		*fields[i] = f
	}
	return s, nil
}

func parseField(part string, lo, hi int) (field, error) {
	if part == "*" {
		return field{any: true}, nil
	}
	f := field{vals: map[int]bool{}}
	for _, seg := range strings.Split(part, ",") {
		step := 0
		base := seg
		if idx := strings.Index(seg, "/"); idx >= 0 {
			n, err := strconv.Atoi(seg[idx+1:])
			if err != nil || n <= 0 {
				return field{}, fmt.Errorf("bad step in %q", seg)
			}
			step = n
			base = seg[:idx]
		}
		start, end := lo, hi
		if base != "*" && base != "" {
			if idx := strings.Index(base, "-"); idx >= 0 {
				a, err1 := strconv.Atoi(base[:idx])
				b, err2 := strconv.Atoi(base[idx+1:])
				if err1 != nil || err2 != nil || a < lo || b > hi || a > b {
					return field{}, fmt.Errorf("bad range %q", base)
				}
				start, end = a, b
			} else {
				v, err := strconv.Atoi(base)
				if err != nil || v < lo || v > hi {
					return field{}, fmt.Errorf("bad value %q", base)
				}
				start, end = v, v
			}
		}
		if step == 0 {
			for v := start; v <= end; v++ {
				f.vals[v] = true
			}
		} else {
			for v := start; v <= end; v += step {
				f.vals[v] = true
			}
		}
	}
	if len(f.vals) == 0 {
		return field{}, fmt.Errorf("empty field %q", part)
	}
	return f, nil
}

func (f field) matches(v int) bool {
	if f.any {
		return true
	}
	return f.vals[v]
}

// Due reports whether the schedule matches the given time.
func (s Schedule) Due(t time.Time) bool {
	return s.Minute.matches(t.Minute()) &&
		s.Hour.matches(t.Hour()) &&
		s.Day.matches(t.Day()) &&
		s.Month.matches(int(t.Month())) &&
		s.Weekday.matches(int(t.Weekday()))
}

// DueTasks returns the tasks whose schedule matches now and
// which have not already run in this same minute.
func (s *Store) DueTasks(now time.Time) []Task {
	var out []Task
	for _, t := range s.Tasks {
		sched, err := Parse(t.Schedule)
		if err != nil {
			continue
		}
		if !sched.Due(now) {
			continue
		}
		if t.LastRun != "" {
			if last, err := time.Parse(time.RFC3339, t.LastRun); err == nil {
				if last.Truncate(time.Minute).Equal(now.Truncate(time.Minute)) {
					continue // already ran this minute
				}
			}
		}
		out = append(out, t)
	}
	return out
}

// MarkRun records that a task ran now.
func (s *Store) MarkRun(name string, now time.Time) {
	for i, t := range s.Tasks {
		if t.Name == name {
			s.Tasks[i].LastRun = now.Format(time.RFC3339)
		}
	}
}
