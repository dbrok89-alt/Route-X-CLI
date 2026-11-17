package cron

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// RunDue executes all tasks that are due and haven't run in the current UTC minute.
// A failing task does not block the others due in the same minute:
// its error is collected and returned at the end (joined), while
// successful tasks are still marked as run.
func (s *Store) RunDue(now time.Time, runFn func(Task) error) (int, error) {
	executed := 0
	var errs []error
	nowMin := now.UTC().Truncate(time.Minute)

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
				if last.UTC().Truncate(time.Minute).Equal(nowMin) {
					continue
				}
			}
		}

		if err := runFn(t); err != nil {
			errs = append(errs, fmt.Errorf("task %q: %w", t.Name, err))
			continue
		}
		s.MarkRun(t.Name, now)
		executed++
	}

	return executed, errors.Join(errs...)
}

// Watch periodically evaluates Load(), RunDue, and Save() until ctx.Done().
func Watch(ctx context.Context, interval time.Duration, runFn func(Task) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case t := <-ticker.C:
			store, err := Load()
			if err != nil {
				continue
			}
			// A failing task must not kill the watcher: report it
			// and keep ticking. Only context cancellation stops
			// Watch.
			if _, err := store.RunDue(t, runFn); err != nil {
				fmt.Fprintf(os.Stderr, "cron watch: %v\n", err)
			}
			_ = store.Save()
		}
	}
}
