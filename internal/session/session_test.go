package session

import (
	"testing"
)

func TestSaveLoadListRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &Session{ID: "20261110-000000", Model: "m", Mode: "default", Started: "2026-11-10T00:00:00Z"}
	s.Messages = append(s.Messages, Message{Role: "user", Content: "hi", Time: "2026-11-10T00:00:00Z"})
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load("20261110-000000")
	if err != nil || len(loaded.Messages) != 1 || loaded.Messages[0].Content != "hi" {
		t.Fatalf("loaded = %+v, err=%v", loaded, err)
	}
	ids, err := List()
	if err != nil || len(ids) != 1 || ids[0] != "20261110-000000" {
		t.Fatalf("ids = %v, err=%v", ids, err)
	}
}

func TestLoadRejectsTraversal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, id := range []string{"../secret", "../../etc/passwd", "a/b", "", "a b"} {
		if _, err := Load(id); err == nil {
			t.Fatalf("Load(%q) should be rejected", id)
		}
	}
}

func TestLoadMissingReturnsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := Load("19990101-000000"); err == nil {
		t.Fatal("missing session should error")
	}
}

func TestSearchFindsSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = Save(&Session{ID: "20261110-100000", Messages: []Message{{Role: "user", Content: "fix the login bug"}}})
	_ = Save(&Session{ID: "20261110-110000", Messages: []Message{{Role: "user", Content: "write a readme"}}})
	matches, err := Search("login")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != "20261110-100000" {
		t.Fatalf("matches = %+v", matches)
	}
	if matches, _ := Search("LOGIN"); len(matches) != 1 {
		t.Fatal("search should be case insensitive")
	}
	if matches, _ := Search("nothing matches this"); len(matches) != 0 {
		t.Fatalf("matches = %+v", matches)
	}
}
