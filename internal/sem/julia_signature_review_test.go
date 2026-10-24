package sem

import "testing"

const juliaRenameTypedParams = "(x::AbstractArray{Float64,3}, y::Dict{Symbol,Vector{Int}}, z::Tuple{String,Bool}, a::Set{UInt8}, b::Ref{ComplexF64}, c::IOBuffer)::Nothing"

func TestJuliaHeaderSignatureIdentityIsRevisioned(t *testing.T) {
	parse := func(source string) []SymbolRecord {
		t.Helper()
		entities, _, status := (TreeSitterParser{}).ParseWithStatus("overloads.jl", source)
		if status.ParseError || len(entities) != 2 {
			t.Fatalf("parse = %#v, status = %#v", entities, status)
		}
		return entitySymbols("local/example", "overloads.jl", "Julia", entities)
	}
	before := parse("f(x::Int) = x + 1\nf(x::String) = length(x)\n")
	after := parse("f(x::String) = length(x)\nf(x::Int) = x + 2\n")
	ids := map[string]string{}
	for _, symbol := range before {
		want := "local/example:Julia:overloads.jl:function:f#sig:" + hash(symbol.Signature)
		if symbol.ID != want {
			t.Fatalf("header identity = %q, want %q", symbol.ID, want)
		}
		ids[symbol.Signature] = symbol.ID
	}
	for _, symbol := range after {
		if symbol.ID != ids[symbol.Signature] {
			t.Errorf("body edit/reordering changed identity: %q -> %q", ids[symbol.Signature], symbol.ID)
		}
	}
	legacyID := "local/example:Julia:overloads.jl:function:f#sig:" + hash("f(x::Int) = x + 1")
	if before[0].ID == legacyID {
		t.Fatal("Julia overload still uses the body-laden signature for identity")
	}
	if IdentityRevision == "4" {
		t.Fatal("Julia header signatures re-key overloads and must invalidate identity revision 4")
	}
}

func TestJuliaOneLineRenameRetainsBodyEvidence(t *testing.T) {
	typedParams := juliaRenameTypedParams
	typedBody := "return transform_payload_into_records(x,y,z,a,b,c)"
	for _, form := range []struct {
		name, before, renamed, replaced string
	}{
		{"short", "encode(x) = x + 1", "send(x) = x + 1", "send(x) = error(\"unavailable\")"},
		{"long", "function encode(x); x + 1; end", "function send(x); x + 1; end", "function send(x); error(\"unavailable\"); end"},
		{"typed_short", "encode" + typedParams + " = transform_payload_into_records(x,y,z,a,b,c)", "send" + typedParams + " = transform_payload_into_records(x,y,z,a,b,c)", "send" + typedParams + " = error(\"unavailable\")"},
		{"typed_long", "function encode" + typedParams + "; " + typedBody + "; end", "function send" + typedParams + "; " + typedBody + "; end", "function send" + typedParams + "; error(\"unavailable\"); end"},
		{"typed_multiline", "function encode" + typedParams + "\n    " + typedBody + "\nend", "function send" + typedParams + "\n    " + typedBody + "\nend", "function send" + typedParams + "\n    error(\"unavailable\")\nend"},
	} {
		t.Run(form.name, func(t *testing.T) {
			parse := func(source string) []Entity {
				t.Helper()
				entities, _, status := (TreeSitterParser{}).ParseWithStatus("rename.jl", source+"\n")
				if status.ParseError || len(entities) != 1 {
					t.Fatalf("parse = %#v, status = %#v", entities, status)
				}
				return entities
			}
			before := parse(form.before)
			changes, removed, added := compareEntities(before, parse(form.replaced), false)
			if len(changes) != 0 || len(removed) != 1 || len(added) != 1 {
				t.Fatalf("unrelated replacement: changes=%#v, removed=%#v, added=%#v", changes, removed, added)
			}
			changes, removed, added = compareEntities(before, parse(form.renamed), false)
			if len(changes) != 1 || changes[0].Type != "renamed" || len(removed) != 0 || len(added) != 0 {
				t.Fatalf("genuine rename: changes=%#v, removed=%#v, added=%#v", changes, removed, added)
			}
		})
	}
}

func TestJuliaTypedRenameFallbackRetainsSimilarBodies(t *testing.T) {
	parse := func(source string) Entity {
		t.Helper()
		entities, _, status := (TreeSitterParser{}).ParseWithStatus("rename.jl", source+"\n")
		if status.ParseError || len(entities) != 1 {
			t.Fatalf("parse = %#v, status = %#v", entities, status)
		}
		return entities[0]
	}
	before := parse("function encode" + juliaRenameTypedParams + "; return transform_payload_into_records(x,y,z,a,b,c); end")
	after := parse("function send" + juliaRenameTypedParams + "; return (transform_payload_into_records(x,y,z,a,b,c)); end")
	if before.Fingerprint == after.Fingerprint || before.BodyHash == after.BodyHash {
		t.Fatal("fixture must exercise fallback similarity, not exact hashes")
	}
	changes, removed, added := compareEntities([]Entity{before}, []Entity{after}, false)
	if len(changes) != 1 || changes[0].Type != "renamed" || len(removed) != 0 || len(added) != 0 {
		t.Fatalf("similar-body rename: changes=%#v, removed=%#v, added=%#v", changes, removed, added)
	}
}

func TestAnalyzeJuliaTypedHeaderReplacement(t *testing.T) {
	for _, replacementPath := range []string{"rename.jl", "other.jl"} {
		t.Run(replacementPath, func(t *testing.T) {
			repo := t.TempDir()
			git(t, repo, "init")
			git(t, repo, "config", "user.name", "Entire Graph Test")
			git(t, repo, "config", "user.email", "graph@example.com")
			write(t, repo, "rename.jl", "function encode"+juliaRenameTypedParams+"; return transform_payload_into_records(x,y,z,a,b,c); end\n")
			git(t, repo, "add", ".")
			git(t, repo, "commit", "-m", "before")
			base := rev(t, repo, "HEAD")
			if replacementPath != "rename.jl" {
				git(t, repo, "rm", "rename.jl")
			}
			write(t, repo, replacementPath, "function send"+juliaRenameTypedParams+"; error(\"unavailable\"); end\n")
			git(t, repo, "add", ".")
			git(t, repo, "commit", "-m", "replace unrelated function")
			result, err := AnalyzeGitRange(t.Context(), repo, base, "HEAD", nil)
			if err != nil {
				t.Fatal(err)
			}
			changes := map[string]string{}
			count := 0
			for _, file := range result.Files {
				for _, change := range file.Changes {
					changes[change.Name] = change.Type
					count++
				}
			}
			if count != 2 || changes["encode"] != "removed" || changes["send"] != "added" {
				t.Fatalf("replacement changes = %#v", result.Files)
			}
		})
	}
}
