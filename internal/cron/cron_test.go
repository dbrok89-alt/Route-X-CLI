package cron

import (
	"testing"
	"time"
)

func mustParse(t *testing.T, expr string) Schedule {
	t.Helper()
	s, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q): %v", expr, err)
	}
	return s
}

func TestParseEveryMinute(t *testing.T) {
	s := mustParse(t, "* * * * *")
	if !s.Due(time.Date(2026, 11, 10, 3, 4, 5, 0, time.UTC)) {
		t.Fatal("every minute should match any time")
	}
}

func TestParseFixedTime(t *testing.T) {
	s := mustParse(t, "30 9 * * *")
	if !s.Due(time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)) {
		t.Fatal("09:30 should match")
	}
	if s.Due(time.Date(2026, 11, 10, 9, 31, 0, 0, time.UTC)) {
		t.Fatal("09:31 should not match")
	}
}

func TestParseStepAndRange(t *testing.T) {
	s := mustParse(t, "*/15 9-17 * * 1-5")
	if !s.Due(time.Date(2026, 11, 9, 10, 30, 0, 0, time.UTC)) { // Monday
		t.Fatal("Mon 10:30 should match")
	}
	if s.Due(time.Date(2026, 11, 9, 10, 31, 0, 0, time.UTC)) {
		t.Fatal("10:31 is not a */15 minute")
	}
	if s.Due(time.Date(2026, 11, 8, 10, 30, 0, 0, time.UTC)) { // Sunday
		t.Fatal("Sunday should not match 1-5")
	}
}

func TestParseList(t *testing.T) {
	s := mustParse(t, "0 8,12,18 * * *")
	if !s.Due(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("12:00 should match")
	}
	if s.Due(time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)) {
		t.Fatal("10:00 should not match")
	}
}

func TestParseInvalid(t *testing.T) {
	for _, bad := range []string{
		"30 9 * *",    // only 4 fields
		"60 9 * * *",  // minute out of range
		"*/0 9 * * *", // zero step
		"9-5 9 * * *", // reversed range
		"abc 9 * * *", // not a number
		"30 25 * * *", // hour out of range
	} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("Parse(%q) should fail", bad)
		}
	}
}

func TestDueTasksSkipsAlreadyRunThisMinute(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)
	s := &Store{Tasks: []Task{
		{Name: "a", Schedule: "30 9 * * *", Prompt: "do a", LastRun: now.Format(time.RFC3339)},
		{Name: "b", Schedule: "30 9 * * *", Prompt: "do b"},
	}}
	due := s.DueTasks(now)
	if len(due) != 1 || due[0].Name != "b" {
		t.Fatalf("due = %+v", due)
	}
}

func TestAddRejectsBadSchedule(t *testing.T) {
	s := &Store{}
	if err := s.Add(Task{Name: "x", Schedule: "99 99 * * *", Prompt: "p"}); err == nil {
		t.Fatal("bad schedule should be rejected at add time")
	}
}
