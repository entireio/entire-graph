package sem

import "testing"

// A parsed identifier with the right spelling is not necessarily the binding
// token. A C tag and function may share a spelling in separate namespaces;
// the function's name line must not come from its preceding return-type tag.
func TestReview302ParserNameBindingToken(t *testing.T) {
	cases := []struct {
		name         string
		source       string
		requireKnown bool
	}{
		{
			name: "same_tag_is_not_the_function_binding",
			source: "struct item { int value; };\n" +
				"struct item\n" +
				"item(void) {\n" +
				"  return (struct item){0};\n" +
				"}\n",
		},
		{
			name: "different_tag_positive_control",
			source: "struct other { int value; };\n" +
				"struct other\n" +
				"item(void) {\n" +
				"  return (struct other){0};\n" +
				"}\n",
			requireKnown: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entities, language := (TreeSitterParser{}).Parse("binding.c", tc.source)
			if language != "C" {
				t.Fatalf("language = %q, want C", language)
			}
			callables := 0
			for _, entity := range entities {
				// The separately emitted struct/tag entity is not this assertion's
				// subject, even in the case where it shares the function's name.
				if entity.Kind != "function" || entity.Name != "item" {
					continue
				}
				callables++
				if entity.nameLine == 0 {
					if tc.requireKnown {
						t.Error("different-tag control did not preserve a known function-name line")
					}
					continue // Unknown is safer than a false binding-token claim.
				}
				if entity.nameLine != 3 {
					t.Errorf("function item nameLine = %d, want authored binding token on line 3 (or unknown 0)", entity.nameLine)
				}
			}
			if callables != 1 {
				t.Fatalf("emitted %d functions named item, want exactly one; entities = %#v", callables, entities)
			}
		})
	}
}
