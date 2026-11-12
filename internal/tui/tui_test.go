package tui

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStripANSIAndVisibleWidth(t *testing.T) {
	colored := Red + "Hello" + Reset + " " + Bold + "World" + Reset
	stripped := StripANSI(colored)
	if stripped != "Hello World" {
		t.Errorf("expected 'Hello World', got '%s'", stripped)
	}

	width := VisibleWidth(colored)
	if width != 11 {
		t.Errorf("expected width 11, got %d", width)
	}

	padded := PadRight("Go", 5)
	if VisibleWidth(padded) != 5 {
		t.Errorf("expected padded width 5, got %d", VisibleWidth(padded))
	}
}

func TestRenderBannerAndStatusBar(t *testing.T) {
	theme := DefaultTheme()
	banner := RenderBanner("Route-X CLI", theme)
	if !strings.Contains(banner, "Route-X CLI") {
		t.Errorf("banner missing title")
	}

	statusOk := Status{Code: 200, Message: "Success", Level: "OK"}
	barOk := RenderStatusBar(statusOk, theme)
	if !strings.Contains(barOk, "200") || !strings.Contains(barOk, "Success") {
		t.Errorf("status bar missing status details")
	}

	statusErr := Status{Code: 500, Message: "Internal Error", Level: "ERROR"}
	barErr := RenderStatusBar(statusErr, theme)
	if !strings.Contains(barErr, "ERROR") {
		t.Errorf("status bar missing error level")
	}

	progress := RenderProgressBar(5, 10, 20, theme)
	if !strings.Contains(progress, "50%") {
		t.Errorf("progress bar expected 50%%, got '%s'", progress)
	}

	emptyProgress := RenderProgressBar(0, 0, 10, theme)
	if !strings.Contains(emptyProgress, "---") {
		t.Errorf("empty progress bar issue")
	}
}

func TestFormatDiffAndTodoBoard(t *testing.T) {
	oldText := "line1\nline2"
	newText := "line1\nline3"
	diff := FormatDiff(oldText, newText)
	if !strings.Contains(diff, "- line1") || !strings.Contains(diff, "+ line1") {
		t.Errorf("FormatDiff output invalid: %s", diff)
	}

	theme := DefaultTheme()
	items := []TodoItem{
		{ID: 1, Title: "Task 1", Done: false},
		{ID: 2, Title: "Task 2", Done: true},
	}
	board := RenderTodoBoard(items, theme)
	if !strings.Contains(board, "[ ] 1. Task 1") || !strings.Contains(board, "[X] 2. Task 2") {
		t.Errorf("RenderTodoBoard output invalid: %s", board)
	}
}

func TestRenderMarkdownAndToolCard(t *testing.T) {
	theme := DefaultTheme()
	md := "# Header 1\n## Header 2\n- Item 1\nNormal text"
	renderedMD := RenderMarkdown(md)
	if !strings.Contains(renderedMD, "Header 1") || !strings.Contains(renderedMD, "• Item 1") {
		t.Errorf("RenderMarkdown failed")
	}

	prompt := PromptString("Enter option")
	if !strings.Contains(prompt, "Enter option >") {
		t.Errorf("PromptString failed")
	}

	status := Status{Code: 200, Message: "Ready", Level: "OK"}
	card := RenderToolCard("Executor", "Executes commands", status, theme)
	if !strings.Contains(card, "Tool: Executor") {
		t.Errorf("RenderToolCard failed")
	}
}

func TestSpinnerStartStopConcurrency(t *testing.T) {
	sp := NewSpinner("Loading...")
	sp.Start()
	sp.UpdateLabel("Processing...")
	time.Sleep(50 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sp.UpdateLabel("Concurrent label")
		}(i)
	}
	wg.Wait()

	sp.Stop()
	sp.Stop() // Test double stop safety
}
