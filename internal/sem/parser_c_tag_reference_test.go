package sem

import "testing"

// TestCTagReferencesAreNotDefinitions pins the phantom definitions that a
// C-family struct or enum tag used to produce. In C the tag name is part of the
// type syntax, so `struct Ledger *l` parses to the same struct_specifier node
// type as `struct Ledger { ... }` — the definition is the one carrying a body.
// Extracting both meant every mention of a type produced another "definition"
// of it, at a different line, competing with the real one in search results and
// in `def`.
func TestCTagReferencesAreNotDefinitions(t *testing.T) {
	t.Parallel()
	entities, language := TreeSitterParser{}.Parse("ledger.c", `struct Ledger {
    int total;
};

enum Mode {
    MODE_FAST,
};

int ledger_add(struct Ledger *ledger, int amount) {
    return ledger->total + amount;
}

int ledger_run(enum Mode mode) {
    struct Ledger ledger = {0};
    return ledger_add(&ledger, (int)mode);
}
`)
	if language != "C" {
		t.Fatalf("language = %q, want C", language)
	}
	counts := map[string]int{}
	for _, entity := range entities {
		counts[entity.Kind+":"+entity.Name]++
	}
	if got := counts["struct:Ledger"]; got != 1 {
		t.Errorf("struct Ledger has %d records, want exactly 1 (the definition): %#v", got, counts)
	}
	if got := counts["enum:Mode"]; got != 1 {
		t.Errorf("enum Mode has %d records, want exactly 1 (the definition): %#v", got, counts)
	}
}

// TestCHeaderPrototypesEmitNoPhantomStructs covers the shape that made this
// worst: a public header is mostly prototypes, so nearly every struct mention
// in it is a parameter type. Each one used to become a struct "definition"
// anchored on the prototype's line.
func TestCHeaderPrototypesEmitNoPhantomStructs(t *testing.T) {
	t.Parallel()
	entities, _ := TreeSitterParser{}.Parse("ledger.h", `struct Ledger;

int ledger_add(struct Ledger *ledger, int amount);
int ledger_reset(struct Ledger *ledger);
`)
	for _, entity := range entities {
		if entity.Kind == "struct" {
			t.Errorf("header with no struct body emitted a struct definition at line %d: %#v", entity.StartLine, entity)
		}
	}
}

// TestCPlusPlusTagReferencesAreNotDefinitions checks the same rule for C++,
// whose grammar shares the specifier nodes.
func TestCPlusPlusTagReferencesAreNotDefinitions(t *testing.T) {
	t.Parallel()
	entities, language := TreeSitterParser{}.Parse("point.cpp", `struct Point {
    int x;
};

int sum(struct Point p) {
    struct Point other = p;
    return other.x;
}
`)
	if language != "C++" {
		t.Fatalf("language = %q, want C++", language)
	}
	structs := 0
	for _, entity := range entities {
		if entity.Kind == "struct" && entity.Name == "Point" {
			structs++
		}
	}
	if structs != 1 {
		t.Errorf("struct Point has %d records, want exactly 1 (the definition)", structs)
	}
}
