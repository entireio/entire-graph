package sem

import (
	"slices"
	"strings"
	"testing"
)

func TestCppSpecializationAliases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		language  string
		symbol    string
		signature string
		want      []string
	}{
		{
			name:      "fmt tuple-join formatter",
			language:  "C++",
			symbol:    "formatter",
			signature: "struct formatter<tuple_join_view<Char, T...>, Char>",
			want:      []string{"tuple_join_view", "formatter<tuple_join_view>"},
		},
		{
			name:      "qualified and cv-decorated argument",
			language:  "C++",
			symbol:    "formatter",
			signature: "struct formatter<const detail::join_view<It, Sentinel, Char>&, Char>",
			want:      []string{"join_view", "formatter<join_view>"},
		},
		{
			name:      "primary template names no argument",
			language:  "C++",
			symbol:    "formatter",
			signature: "struct formatter : detail::fallback_formatter<T, Char>",
			want:      nil,
		},
		{
			name:      "leading template parameter is not a discriminator",
			language:  "C++",
			symbol:    "formatter",
			signature: "struct formatter<T, Char, enable_if_t<is_integral<T>::value>>",
			want:      nil,
		},
		{
			name:      "self-specialization adds nothing",
			language:  "C++",
			symbol:    "formatter",
			signature: "struct formatter<formatter>",
			want:      nil,
		},
		{
			name:      "non-C++ language is untouched",
			language:  "Rust",
			symbol:    "Formatter",
			signature: "impl Formatter<TupleJoinView>",
			want:      nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := cppSpecializationAliases(tc.language, tc.symbol, tc.signature)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("cppSpecializationAliases(%q, %q) = %v, want %v", tc.symbol, tc.signature, got, tc.want)
			}
		})
	}
}

// The aliases must reach the snapshot for both the specialization and its
// members, and must NOT change the symbol's name, qualified name or stable ID —
// the compound-v1 ID is derived from the qualified name, so renaming the symbol
// would have re-keyed every specialization in every indexed repository.
func TestCppSpecializationAliasesInSnapshotKeepIDsStable(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "include/fmt/ranges.h", `#ifndef FMT_RANGES_H_
#define FMT_RANGES_H_

template <typename Char, typename... T> struct tuple_join_view {
  const std::tuple<T...>& tuple;
  basic_string_view<Char> sep;
};

template <typename Char, typename... T>
struct formatter<tuple_join_view<Char, T...>, Char> {
  template <typename ParseContext>
  auto parse(ParseContext& ctx) -> decltype(ctx.begin()) {
    return ctx.begin();
  }

  template <typename FormatContext>
  auto format(const tuple_join_view<Char, T...>& value, FormatContext& ctx) ->
      typename FormatContext::iterator {
    return ctx.out();
  }
};

#endif
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	var specialization, member *SymbolRecord
	for i, symbol := range snapshot.Symbols {
		if symbol.FilePath != "include/fmt/ranges.h" {
			continue
		}
		switch {
		case symbol.QualifiedName == "formatter" && typeLikeKind(symbol.Kind):
			specialization = &snapshot.Symbols[i]
		case symbol.QualifiedName == "formatter.parse":
			member = &snapshot.Symbols[i]
		}
	}
	if specialization == nil {
		t.Fatalf("no symbol for the formatter specialization: %#v", snapshot.Symbols)
	}
	if !slices.Contains(specialization.Aliases, "tuple_join_view") ||
		!slices.Contains(specialization.Aliases, "formatter<tuple_join_view>") {
		t.Fatalf("specialization aliases = %v, want the specialization argument and the compound form", specialization.Aliases)
	}
	// Name identity — and therefore the stable ID — is unchanged.
	if specialization.Name != "formatter" || specialization.QualifiedName != "formatter" {
		t.Fatalf("specialization renamed to %q/%q; compound-v1 IDs must stay stable", specialization.Name, specialization.QualifiedName)
	}
	if !strings.HasSuffix(specialization.ID, ":formatter") {
		t.Fatalf("specialization ID %q no longer derives from the unchanged qualified name", specialization.ID)
	}
	if specialization.StableIDVersion != StableSymbolIDVersion {
		t.Fatalf("stable ID version = %q, want %q", specialization.StableIDVersion, StableSymbolIDVersion)
	}
	if member == nil {
		t.Fatalf("no symbol for the specialization's parse member")
	}
	if !slices.Contains(member.Aliases, "tuple_join_view") {
		t.Fatalf("member aliases = %v, want the container's specialization argument", member.Aliases)
	}
}

// The alias is what carries the name signal: a query naming the specialized-on
// type must score the specialization above an unrelated declaration of the same
// generic name.
func TestCppSpecializationAliasScoresNameSignal(t *testing.T) {
	t.Parallel()
	q := buildSearchQuery("tuple_join_view formatter")
	specialization := SymbolRecord{
		Kind: "struct", Name: "formatter", QualifiedName: "formatter",
		Signature: "struct formatter<tuple_join_view<Char, T...>, Char>",
		Language:  "C++",
		Aliases:   cppSpecializationAliases("C++", "formatter", "struct formatter<tuple_join_view<Char, T...>, Char>"),
	}
	plain := SymbolRecord{
		Kind: "struct", Name: "formatter", QualifiedName: "formatter",
		Signature: "struct formatter", Language: "C++",
	}
	specializationScore, signals := symbolSearchScore(q, specialization)
	plainScore, _ := symbolSearchScore(q, plain)
	if specializationScore <= plainScore {
		t.Fatalf("specialization score %v does not beat the same-named primary template %v", specializationScore, plainScore)
	}
	if !slices.Contains(signals, "alias") {
		t.Fatalf("signals = %v, want an alias signal", signals)
	}
}
