package sem

import "testing"

func TestShellCommandCallIdentifiers(t *testing.T) {
	got := shellCommandCallIdentifiers(`function dirhistory_back() {
  local cw=""
  setopt localoptions no_ksh_arrays

  pop_past cw
  if [[ "" == "$cw" ]]; then
    dirhistory_past=($PWD)
    return
  fi

  pop_past d
  if [[ "" != "$d" ]]; then
    dirhistory_cd $d && push_future $cw
  else
    push_past $cw
  fi

  DIRHISTORY_CD="1" refresh_prompt
  reply=(not_a_call also_not_a_call)
  result=$(compute_value "$cw")
  cat <<EOF
usage: not-a-call [options]
inner_doc_word
EOF
  zle .kill-buffer
  echo "pop_past inside a string" # trailing pop_future comment
}
`)
	for _, want := range []string{
		"pop_past",       // plain command
		"dirhistory_cd",  // after `then`, before &&
		"push_future",    // after &&
		"push_past",      // after `else`
		"refresh_prompt", // after a VAR=x env prefix
		"compute_value",  // inside $(...)
	} {
		if _, ok := got[want]; !ok {
			t.Fatalf("missing command-position identifier %q in %#v", want, got)
		}
	}
	for _, reject := range []string{
		"return", "setopt", "local", "echo", "zle", "cat", // builtins/keywords are ignored
		"function", "dirhistory_back", "if", "then", "else", "fi",
		"cw", "d", "DIRHISTORY_CD", "dirhistory_past", "reply", "result", // assignments
		"not_a_call", "also_not_a_call", // array literal elements
		"usage", "inner_doc_word", // heredoc body
		"not-a-call",
		"pop_future", // only mentioned in a comment
		".kill-buffer",
	} {
		if _, ok := got[reject]; ok {
			t.Fatalf("unexpected identifier %q collected as a command: %#v", reject, got)
		}
	}
	if _, ok := got["pop_past"]; !ok {
		t.Fatalf("quoted mention should not have removed the real call: %#v", got)
	}
}
