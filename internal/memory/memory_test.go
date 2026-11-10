package memory

import (
	"strings"
	"testing"
)

func TestRememberAndSearch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := Remember("bos suka Go"); err != nil {
		t.Fatal(err)
	}
	if got := Read("MEMORY.md"); !strings.Contains(got, "bos suka Go") {
		t.Fatalf("memory = %q", got)
	}
	if found := Search("go"); len(found) != 1 {
		t.Fatalf("search = %v", found)
	}
	if found := Search("tidak ada"); len(found) != 0 {
		t.Fatalf("search should be empty, got %v", found)
	}
}

func TestRememberIsBounded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for i := 0; i < 200; i++ {
		if _, err := Remember("fakta nomor yang cukup panjang untuk mengisi memori dengan cepat 1234567890"); err != nil {
			t.Fatal(err)
		}
	}
	if got := Read("MEMORY.md"); len(got) > MaxMemoryChars {
		t.Fatalf("memory grew past bound: %d chars", len(got))
	}
}
