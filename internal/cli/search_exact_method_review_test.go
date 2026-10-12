package cli

import "testing"

func TestExactNameMethodDoesNotClaimReturnType(t *testing.T) {
	if exactNameLineDefines("func (r *T) Build() Result", "Result", ".go") {
		t.Fatal("return type called a definition")
	}
	if !exactNameLineDefines("func (r *T) Build() Result", "Build", ".go") {
		t.Fatal("method definition lost")
	}
}
