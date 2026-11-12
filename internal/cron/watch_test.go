package cron

import (
	"context"
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
