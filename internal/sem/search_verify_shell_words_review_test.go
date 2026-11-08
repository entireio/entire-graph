package sem

import "testing"

func TestSearchVerifyScriptPreservesQuotedWordBoundaries(t *testing.T) {
	for _, tc := range []struct {
		script string
		want   bool
	}{
		{`MSG="hello jest world" echo done`, false},
		{`MSG="hello jest world" jest`, true},
		{`MSG='hello jest world' echo done`, false},
		{`MSG=hello\ jest\ world echo done`, false},
		{`cross-env MSG="hello jest world" jest`, true},
		{"FOO=`echo a; echo b` jest", true},
		{"cross-env FOO=`echo a jest b` jest", true},
		{"FOO=`echo a jest b` true", false},
		{"FOO=`echo a; echo b` true", false},
		{"`jest`", false},
		{"`echo jest`", false},
		{"FOO=`echo \\`quoted\\` value` jest", true},
		{"FOO=`unterminated jest", false},
		{`bunx jest`, true}, {`bun run jest`, true}, {`env run jest`, false},
		{`echo "jest"`, false}, {`"jest"`, true},
	} {
		if got := searchVerifyScriptInvokesRunner(tc.script, "jest"); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.script, got, tc.want)
		}
	}
}
