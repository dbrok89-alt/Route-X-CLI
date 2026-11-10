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

func TestEditFileExact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	_ = os.WriteFile(path, []byte("package main\n\n// TODO: fix\n"), 0o644)
	diff, written, err := EditFile(path, "// TODO: fix", "// DONE: fixed", "auto", AutoApprove)
	if err != nil || !written {
		t.Fatalf("diff=%q written=%v err=%v", diff, written, err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "// DONE: fixed") {
		t.Fatalf("file = %q", got)
	}
}

func TestEditFileRefusesAmbiguous(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	_ = os.WriteFile(path, []byte("x\ny\nx\n"), 0o644)
	if _, _, err := EditFile(path, "x", "z", "auto", AutoApprove); err == nil {
		t.Fatal("ambiguous old_text should be refused")
	}
}

func TestEditFileNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	_ = os.WriteFile(path, []byte("hello\n"), 0o644)
	if _, _, err := EditFile(path, "missing", "z", "auto", AutoApprove); err == nil {
		t.Fatal("missing old_text should be refused")
	}
}

func TestEditFileTrimmedFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	_ = os.WriteFile(path, []byte("package main\n\n\t// TODO: fix\n"), 0o644)
	// old_text without the tab indent: exact match fails, the
	// whitespace-tolerant fallback must still find it.
	_, written, err := EditFile(path, "// TODO: fix", "// DONE", "auto", AutoApprove)
	if err != nil || !written {
		t.Fatalf("written=%v err=%v", written, err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "// DONE") {
		t.Fatalf("file = %q", got)
	}
}

func TestEditFilePlanModeWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	_ = os.WriteFile(path, []byte("hello\n"), 0o644)
	_, written, err := EditFile(path, "hello", "bye", "plan", AutoApprove)
	if err != nil || written {
		t.Fatalf("written=%v err=%v", written, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello\n" {
		t.Fatalf("plan mode changed the file: %q", got)
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

func TestGlob(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"a.go", "sub/b.go", "sub/deep/c.go", "sub/d.txt", "top.md"} {
		full := filepath.Join(dir, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte("x"), 0o644)
	}
	must := func(pattern string, want ...string) {
		t.Helper()
		got, err := Glob(dir, pattern)
		if err != nil {
			t.Fatalf("Glob(%q): %v", pattern, err)
		}
		if len(got) != len(want) {
			t.Fatalf("Glob(%q) = %v, want %v", pattern, got, want)
		}
		for i := range want {
			if got[i] != filepath.Join(dir, want[i]) {
				t.Fatalf("Glob(%q)[%d] = %q, want %q", pattern, i, got[i], filepath.Join(dir, want[i]))
			}
		}
	}
	must("*.go", "a.go")
	must("**/*.go", "a.go", "sub/b.go", "sub/deep/c.go")
	must("sub/*.go", "sub/b.go")
	must("sub/**/*.go", "sub/b.go", "sub/deep/c.go")
	must("*.md", "top.md")
	must("nope/*.go")
	if _, err := Glob(dir, "../*.go"); err == nil {
		t.Fatal("Glob must refuse patterns escaping the workspace")
	}
}

func TestApplyPatch(t *testing.T) {
	dir := t.TempDir()
	oldWd, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer os.Chdir(oldWd)
	_ = os.WriteFile("old.go", []byte("package main\n"), 0o644)
	_ = os.WriteFile("gone.go", []byte("x\n"), 0o644)

	patch := "*** Add File: new.go\npackage main\n\n*** Update File: old.go\npackage main\n// changed\n\n*** Delete File: gone.go\n*** Rename File: old.go -> renamed.go\n"
	ops, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 4 {
		t.Fatalf("ops = %d, want 4", len(ops))
	}
	diff, written, err := ApplyPatch(ops, "auto", AutoApprove)
	if err != nil || !written {
		t.Fatalf("ApplyPatch: diff=%q written=%v err=%v", diff, written, err)
	}
	if _, err := os.Stat("new.go"); err != nil {
		t.Fatal("new.go not created")
	}
	if _, err := os.Stat("renamed.go"); err != nil {
		t.Fatal("renamed.go not created")
	}
	if _, err := os.Stat("gone.go"); err == nil {
		t.Fatal("gone.go not deleted")
	}
	data, _ := os.ReadFile("renamed.go")
	if string(data) != "package main\n// changed\n\n" {
		t.Fatalf("renamed.go = %q", data)
	}
	// Plan mode writes nothing.
	diff, written, err = ApplyPatch([]PatchOp{{Kind: "add", Path: "plan.go", Body: "x\n"}}, "plan", AutoApprove)
	if err != nil || written {
		t.Fatalf("plan ApplyPatch: written=%v err=%v", written, err)
	}
	if _, err := os.Stat("plan.go"); err == nil {
		t.Fatal("plan mode wrote a file")
	}
	// Refusals: add existing, update missing, delete missing,
	// rename to existing, path outside the workspace.
	for _, bad := range []PatchOp{
		{Kind: "add", Path: "new.go", Body: "x\n"},
		{Kind: "update", Path: "missing.go", Body: "x\n"},
		{Kind: "delete", Path: "missing.go"},
		{Kind: "rename", Path: "new.go", Dest: "renamed.go"},
		{Kind: "add", Path: "../escape.go", Body: "x\n"},
	} {
		if _, _, err := ApplyPatch([]PatchOp{bad}, "auto", AutoApprove); err == nil {
			t.Fatalf("ApplyPatch(%+v) should have failed", bad)
		}
	}
	if _, err := ParsePatch("*** Frobnicate File: x.go\n"); err == nil {
		t.Fatal("ParsePatch must reject unknown markers")
	}
	if _, err := ParsePatch("orphan content\n"); err == nil {
		t.Fatal("ParsePatch must reject content outside any operation")
	}
}
