package cron

import (
	"context"
	"time"
)

// RunDue executes all tasks that are due and haven't run in the current UTC minute.
func (s *Store) RunDue(now time.Time, runFn func(Task) error) (int, error) {
	executed := 0
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
			return executed, err
		}
		s.MarkRun(t.Name, now)
		executed++
	}

	return executed, nil
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
			if _, err := store.RunDue(t, runFn); err != nil {
				return err
			}
			_ = store.Save()
		}
	}
}
