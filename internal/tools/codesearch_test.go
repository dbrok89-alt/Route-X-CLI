package tools

import (
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
