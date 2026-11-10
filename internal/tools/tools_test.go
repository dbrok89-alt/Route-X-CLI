package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffNewFile(t *testing.T) {
	d := Diff("", "a\nb\n")
	if d != "+ a\n+ b\n" {
		t.Fatalf("new file diff = %q", d)
	}
}

func TestDiffKeepsOrderAndDuplicates(t *testing.T) {
	// Old set-based diff hid reordered and duplicated lines.
	d := Diff("a\nb\na\n", "b\na\na\n")
	// Same multiset, different order: a real diff must show it.
	// LCS diff shows the move as -a/+a, not nothing.
	if d == "" || (!strings.Contains(d, "- a") && !strings.Contains(d, "+ a")) {
		t.Fatalf("reorder diff = %q, want a move shown", d)
	}
	d = Diff("x\nx\n", "x\n")
	if d != "- x\n" {
		t.Fatalf("duplicate removal diff = %q, want \"- x\\n\"", d)
	}
}

func TestDiffChangedLine(t *testing.T) {
	d := Diff("a\nb\nc\n", "a\nB\nc\n")
	if d != "- b\n+ B\n" {
		t.Fatalf("diff = %q", d)
	}
}

func TestWriteFilePlanNeverWrites(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	path := "x.go"
	_, written, err := WriteFile(path, "package main\n", "plan", AutoApprove)
	if err != nil || written {
		t.Fatalf("plan wrote: written=%v err=%v", written, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("plan mode created a file")
	}
}

func TestWriteFileRefusesOutsideProject(t *testing.T) {
	for _, p := range []string{"/tmp/evil.go", "../evil.go", "/etc/passwd"} {
		_, _, err := WriteFile(p, "x", "auto", AutoApprove)
		if err == nil {
			t.Fatalf("WriteFile(%q) should refuse", p)
		}
	}
}

func TestRunCommandBlocked(t *testing.T) {
	for _, cmd := range []string{"rm -rf /tmp/x", "rm -fr /", "git push --force", "shutdown now", ""} {
		if _, err := RunCommand(cmd, "auto", AutoApprove); err == nil {
			t.Fatalf("RunCommand(%q) should be refused", cmd)
		}
	}
}

func TestRunCommandPlan(t *testing.T) {
	out, err := RunCommand("echo hi", "plan", AutoApprove)
	if err != nil || !strings.Contains(out, "plan mode") {
		t.Fatalf("plan run = %q, %v", out, err)
	}
}

func TestSearchFindsAndSkipsDirs(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("hello TODO\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".git", "b.go"), []byte("TODO hidden\n"), 0o644)
	matches, err := Search(dir, "TODO")
	if err != nil || len(matches) != 1 {
		t.Fatalf("matches=%v err=%v", matches, err)
	}
}

func TestReadFileTooLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	_ = os.WriteFile(path, make([]byte, (1<<20)+1), 0o644)
	if _, err := ReadFile(path); err == nil {
		t.Fatal("oversize file should be refused")
	}
}

func TestListFilesRespectsLimit(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		_ = os.WriteFile(filepath.Join(dir, "f"+string(rune('a'+i))+".txt"), []byte("x"), 0o644)
	}
	if got := len(ListFiles(dir, 3)); got != 3 {
		t.Fatalf("ListFiles limit: got %d, want 3", got)
	}
}
