package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateAndRestore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("keep\n"), 0o644)

	id, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Change and delete, then add a new file.
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("v2\n"), 0o644)
	_ = os.Remove(filepath.Join(dir, "b.txt"))
	_ = os.WriteFile(filepath.Join(dir, "c.txt"), []byte("new\n"), 0o644)

	if err := Restore(dir, id); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "v1\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if _, err := os.ReadFile(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal("b.txt should be back")
	}
	if _, err := os.Stat(filepath.Join(dir, "c.txt")); !os.IsNotExist(err) {
		t.Fatal("c.txt (created after checkpoint) should be gone")
	}
}

func TestRestoreUnknownID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := Restore(t.TempDir(), "20000101-000000"); err == nil {
		t.Fatal("unknown checkpoint should fail")
	}
}

func TestListNewestFirst(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	if _, err := Create(dir); err != nil {
		t.Fatal(err)
	}
	list := List()
	if len(list) != 1 || list[0].Files[0] != "a.txt" {
		t.Fatalf("list = %+v", list)
	}
}
