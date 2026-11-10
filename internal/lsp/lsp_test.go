package lsp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fakeServer(t *testing.T) string {
	t.Helper()
	py := "python3"
	if runtime.GOOS == "windows" {
		py = "python"
	}
	script, err := filepath.Abs("testdata/lsp_fake.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatal(err)
	}
	return py + " " + script
}

func TestLSPClientAgainstFakeServer(t *testing.T) {
	c, err := Start(fakeServer(t), t.TempDir())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	defs, err := c.Definition(ctx, "/tmp/a.go", 1, 2)
	if err != nil || len(defs) != 1 || defs[0].Line != 9 || !strings.HasSuffix(defs[0].URI, "def.go") {
		t.Fatalf("definition: %+v err=%v", defs, err)
	}
	refs, err := c.References(ctx, "/tmp/a.go", 1, 2)
	if err != nil || len(refs) != 2 {
		t.Fatalf("references: %+v err=%v", refs, err)
	}
	hover, err := c.Hover(ctx, "/tmp/a.go", 1, 2)
	if err != nil || hover != "func Foo() int" {
		t.Fatalf("hover: %q err=%v", hover, err)
	}
	syms, err := c.DocumentSymbol(ctx, "/tmp/a.go")
	if err != nil || len(syms) != 2 || syms[0].Name != "Foo" || syms[1].Name != "Bar" {
		t.Fatalf("symbols: %+v err=%v", syms, err)
	}
}

func TestLSPStartMissingBinary(t *testing.T) {
	if _, err := Start("definitely-not-a-real-lsp-server-xyz", t.TempDir()); err == nil {
		t.Fatal("Start must fail for a missing binary")
	}
}
