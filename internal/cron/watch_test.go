package cron

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRunDueAndWatch(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	store := &Store{
		Tasks: []Task{
			{
				Name:     "test-task",
				Schedule: "* * * * *",
			},
		},
	}

	executed := 0
	runFn := func(task Task) error {
		executed++
		return nil
	}

	n, err := store.RunDue(now, runFn)
	if err != nil {
		t.Fatalf("RunDue returned error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 task executed, got %d", n)
	}
	if executed != 1 {
		t.Errorf("expected runFn called 1 time, got %d", executed)
	}

	// Test double execution prevention in the same UTC minute
	n2, err := store.RunDue(now, runFn)
	if err != nil {
		t.Fatalf("RunDue second execution returned error: %v", err)
	}
	if n2 != 0 {
		t.Errorf("expected 0 tasks executed on second call in same minute, got %d", n2)
	}

	// Test Watch cancellation via context.WithCancel
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	errWatch := Watch(ctx, 10*time.Millisecond, runFn)
	if errWatch == nil {
		t.Errorf("expected error from Watch with cancelled context, got nil")
	}
}

func TestRunDueContinuesPastFailingTask(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	store := &Store{Tasks: []Task{
		{Name: "bad-task", Schedule: "* * * * *"},
		{Name: "good-task", Schedule: "* * * * *"},
	}}
	ran := map[string]bool{}
	runFn := func(task Task) error {
		ran[task.Name] = true
		if task.Name == "bad-task" {
			return fmt.Errorf("boom")
		}
		return nil
	}
	n, err := store.RunDue(now, runFn)
	if err == nil {
		t.Fatal("expected an error from the failing task")
	}
	if !strings.Contains(err.Error(), "bad-task") {
		t.Errorf("error should name the failing task, got %v", err)
	}
	if n != 1 || !ran["good-task"] {
		t.Errorf("good task must still run: n=%d ran=%v", n, ran)
	}
	if store.Tasks[1].LastRun == "" {
		t.Error("good task should be marked as run")
	}
	if store.Tasks[0].LastRun != "" {
		t.Error("failed task must not be marked as run")
	}
}

func TestWatchContinuesAfterTaskError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A due task in the on-disk store, so Watch's tick path runs it.
	store := &Store{Tasks: []Task{{Name: "flaky", Schedule: "* * * * *"}}}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runFn := func(task Task) error {
		calls++
		return fmt.Errorf("boom")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	err := Watch(ctx, 10*time.Millisecond, runFn)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if calls < 2 {
		t.Errorf("watcher must keep ticking past task errors, runFn called %d times", calls)
	}
}
