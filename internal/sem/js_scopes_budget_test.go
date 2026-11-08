package sem

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// buildJSScanState gets its budget from the CALLER's context, so a caller near
// its own deadline can leave nothing for this file. tree-sitter reports
// cancellation only if it polls before the parse finishes, so the exhausted
// budget used to be visible only when the parse happened to be slow enough --
// a fast parse returned a valid tree, no error, and nothing recorded that the
// deadline had already passed. That made E_PARSE_TIMEOUT a coin flip, which is
// why TestTypeScriptRelationScanParseTimeoutRecordsPartialFailure and
// TestStreamSnapshotFreshBuildReportsNoDuplicateFailures both failed in a full
// serialized ./internal/sem run while passing on their own.
func TestScopeParseWithNoBudgetLeftIsReportedNotAttempted(t *testing.T) {
	const src = "export function run() {}\n"
	grammar := typescript.GetLanguage()

	t.Run("exhausted budget is a timeout", func(t *testing.T) {
		saved := jsScanParseTimeout
		jsScanParseTimeout = 1 * time.Nanosecond
		defer func() { jsScanParseTimeout = saved }()

		state, err := buildJSScanState(t.Context(), grammar, []byte(src), src)
		if err == nil {
			t.Fatal("a parse with no budget left reported success")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want it to wrap context.DeadlineExceeded so the caller records E_PARSE_TIMEOUT", err)
		}
		if state == nil || state.parsed {
			t.Errorf("state.parsed = true; nothing was parsed within the budget")
		}
	})

	t.Run("a caller already done is attributed to the caller", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := buildJSScanState(ctx, grammar, []byte(src), src)
		if err == nil || !strings.Contains(err.Error(), "stopped by caller") {
			t.Errorf("err = %v, want it attributed to the caller, not to the parse budget", err)
		}
	})

	t.Run("a real budget still parses", func(t *testing.T) {
		state, err := buildJSScanState(t.Context(), grammar, []byte(src), src)
		if err != nil {
			t.Fatalf("a parse with the normal budget failed: %v", err)
		}
		if !state.parsed {
			t.Error("state.parsed = false with a full budget; the check fires too eagerly")
		}
	})
}
