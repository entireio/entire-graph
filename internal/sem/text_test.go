package sem

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteTextPlainWhenNotTerminal(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("NO_COLOR", "")

	var out bytes.Buffer
	WriteText(&out, sampleTextResult())
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("non-terminal output should not contain ANSI escapes:\n%q", out.String())
	}
	if !strings.Contains(out.String(), "~ function validate_token signature changed (1 dependent)") {
		t.Fatalf("plain output missing semantic change:\n%s", out.String())
	}
}

func TestWriteTextColorCanBeForced(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "")

	var out bytes.Buffer
	WriteText(&out, sampleTextResult())
	if !strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("forced color output missing ANSI escapes:\n%q", out.String())
	}
	if !strings.Contains(out.String(), "validate_token") {
		t.Fatalf("colored output missing semantic change:\n%s", out.String())
	}
}

func TestNoColorOverridesForcedColor(t *testing.T) {
	t.Setenv("ENTIRE_GRAPH_FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	WriteText(&out, sampleTextResult())
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("NO_COLOR output should not contain ANSI escapes:\n%q", out.String())
	}
}

func sampleTextResult() Result {
	return Result{
		Base: "HEAD~1",
		Head: "HEAD",
		Files: []FileChange{{
			Path:     "auth.py",
			Language: "Python",
			Changes: []EntityChange{{
				Type:            "signature_changed",
				Kind:            "function",
				Name:            "validate_token",
				DependentsCount: 1,
			}},
		}},
	}
}
