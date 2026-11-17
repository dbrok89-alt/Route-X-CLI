package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodeSearch(t *testing.T) {
	dir := t.TempDir()

	content := `package main

// comment mentioning ProcessData
func ProcessData(data string) {
	// call ProcessData here
}
`
	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	matches, err := CodeSearch(dir, "ProcessData", 10)
	if err != nil {
		t.Fatalf("CodeSearch returned error: %v", err)
	}

	if len(matches) == 0 {
		t.Fatalf("expected matches, got none")
	}

	if !strings.Contains(matches[0].Text, "func ProcessData") {
		t.Errorf("expected top match to be function declaration, got: %q", matches[0].Text)
	}
}

func TestCodeSearchEmptyQuery(t *testing.T) {
	if _, err := CodeSearch(t.TempDir(), "   ", 10); err == nil {
		t.Fatal("empty query must be an error, not a match-everything scan")
	}
}

func TestCodeSearchSkipsLargeAndVendorFiles(t *testing.T) {
	dir := t.TempDir()

	// A 2 MiB file containing the query must be skipped (same 1 MiB
	// cap as tools.Search), so a compiled binary never yields junk.
	big := make([]byte, 2<<20)
	copy(big, []byte("NeedleHere "))
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	// Files under skipDirs (vendor/) must not be scanned either.
	vendorDir := filepath.Join(dir, "vendor", "pkg")
	if err := os.MkdirAll(vendorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vendorDir, "v.go"), []byte("package pkg\n// NeedleHere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// One legitimate small match, so an empty result proves the
	// skips happened rather than the search being broken.
	if err := os.WriteFile(filepath.Join(dir, "ok.go"), []byte("package main\n// NeedleHere\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	matches, err := CodeSearch(dir, "NeedleHere", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 match (the small file), got %d: %v", len(matches), matches)
	}
	if !strings.HasSuffix(matches[0].File, "ok.go") {
		t.Errorf("expected match in ok.go, got %q", matches[0].File)
	}
}

func TestCodeSearchLimit(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		content := "package main\n// marker line\n"
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.go", i)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	matches, err := CodeSearch(dir, "marker", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches with limit 2, got %d", len(matches))
	}
}
