package sem

import "testing"

// The qualifier and binding can share a spelling. A positive name line must
// point to the terminal member token, not the earlier table qualifier.
func TestReview302QualifiedNameBindingToken(t *testing.T) {
	cases := []struct {
		name         string
		qualifier    string
		requireKnown bool
	}{
		{name: "same_spelled_qualifier", qualifier: "A"},
		{name: "different_qualifier_control", qualifier: "B", requireKnown: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := "local " + tc.qualifier + " = {}\nfunction " + tc.qualifier + ".\nA()\nend\n"
			entities, language := (TreeSitterParser{}).Parse("qualified.lua", source)
			if language != "Lua" {
				t.Fatalf("language = %q, want Lua", language)
			}
			wantName := tc.qualifier + ".A"
			callables := 0
			for _, entity := range entities {
				if entity.Kind != "function" || entity.Name != wantName {
					continue
				}
				callables++
				if entity.nameLine == 0 {
					if tc.requireKnown {
						t.Error("different-qualifier control lost the known binding coordinate")
					}
					continue // Unknown is allowed; a false positive coordinate is not.
				}
				if entity.nameLine != 3 {
					t.Errorf("%s nameLine = %d, want terminal binding token on line 3 (or unknown 0)", wantName, entity.nameLine)
				}
			}
			if callables != 1 {
				t.Fatalf("emitted %d functions named %s, want exactly one; entities = %#v", callables, wantName, entities)
			}
		})
	}
}
