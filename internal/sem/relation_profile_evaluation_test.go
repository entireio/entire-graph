package sem

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// A bounded, opt-in P1.5 cost audit. The raw probes overlap the complete
// relation pass; their times are not additive phases or end-to-end savings.
func TestRelationProfileEvaluation(t *testing.T) {
	output := os.Getenv("ENTIRE_GRAPH_RELATION_PROFILE")
	if output == "" {
		t.Skip("set ENTIRE_GRAPH_RELATION_PROFILE for the isolated relation audit")
	}
	out, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	encoder := json.NewEncoder(out)
	for _, language := range []string{"Go", "TypeScript", "Python"} {
		repo := t.TempDir()
		contents := map[string]string{}
		hasher := sha256.New()
		for file := 0; file < 40; file++ {
			ext, header := ".go", "package fixture\nimport \"fmt\"\n"
			if language == "TypeScript" {
				ext, header = ".ts", "import { external } from './dependency';\n"
			}
			if language == "Python" {
				ext, header = ".py", "from dependency import external\n"
			}
			body := header
			for function := 0; function < 10; function++ {
				name := fmt.Sprintf("F%03d_%02d", file, function)
				target := fmt.Sprintf("F%03d_%02d", file, (function+9)%10)
				switch language {
				case "Go":
					body += fmt.Sprintf("func %s(value int) int {\n fmt.Println(value)\n return %s(value)\n}\n", name, target)
				case "TypeScript":
					body += fmt.Sprintf("export function %s(value: number): number {\n external(value);\n return %s(value);\n}\n", name, target)
				case "Python":
					body += fmt.Sprintf("def %s(value):\n    external(value)\n    return %s(value)\n", name, target)
				}
			}
			path := fmt.Sprintf("file%03d%s", file, ext)
			contents[path] = body
			fmt.Fprintf(hasher, "%d:%s%d:%s", len(path), path, len(body), body)
			if err := os.WriteFile(filepath.Join(repo, path), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "relation-cost-audit", ProviderSnapshotOptions{Worktree: true, Profile: ProfileFull})
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Header.PartialFailures) > 0 {
			t.Fatalf("audit fixture incomplete: %+v", snapshot.Header.PartialFailures)
		}
		records := map[string][]SymbolRecord{}
		byName := map[string][]SymbolRecord{}
		imports := map[string][]string{}
		for _, symbol := range snapshot.Symbols {
			records[symbol.FilePath] = append(records[symbol.FilePath], symbol)
			byName[symbol.Name] = append(byName[symbol.Name], symbol)
		}
		var blocks []string
		type resolutionProbe struct {
			name string
			from SymbolRecord
		}
		var resolutions []resolutionProbe
		for _, file := range snapshot.Files {
			content := contents[file.Path]
			imports[file.Path] = importsFor(file.Path, content)
			lines := strings.Split(content, "\n")
			for _, symbol := range records[file.Path] {
				if typeLikeKind(symbol.Kind) {
					continue
				}
				block := symbolBlockFromLines(lines, symbol)
				if language == "TypeScript" {
					if exact, ok := exactSymbolSource(content, symbol); ok {
						block = exact
					}
				}
				blocks = append(blocks, block)
				names := sortedKeysOf(callLikeIdentifiers(block, language))
				for _, name := range names {
					resolutions = append(resolutions, resolutionProbe{name, symbol})
				}
			}
		}
		read := func(path string) (string, bool) { value, ok := contents[path]; return value, ok }
		relationPass := func(precomputed map[string][]string) []RelationRecord {
			var relations []RelationRecord
			forEachRelation(t.Context(), "relation-audit", snapshot.Files, records, read, precomputed, resolveProfile(ProfileFull), 1, nil, func(relation RelationRecord) { relations = append(relations, relation) }, func(failure PartialFailure) { t.Fatalf("relation failure: %+v", failure) })
			return relations
		}
		baseline := relationPass(nil)
		precomputed := relationPass(imports)
		if !reflect.DeepEqual(baseline, precomputed) {
			t.Fatal("precomputed raw imports changed relation output")
		}
		digest := hex.EncodeToString(hasher.Sum(nil))
		for trial := 0; trial < 30; trial++ {
			phases := []string{"importsFor", "callLikeIdentifiers", "resolveCallTargets", "forEachRelation", "forEachRelation_precomputed_imports"}
			if trial%2 == 1 {
				sort.Sort(sort.Reverse(sort.StringSlice(phases)))
			}
			for _, phase := range phases {
				count := 0
				started := time.Now()
				switch phase {
				case "importsFor":
					for _, file := range snapshot.Files {
						count += len(importsFor(file.Path, contents[file.Path]))
					}
				case "callLikeIdentifiers":
					for _, block := range blocks {
						count += len(callLikeIdentifiers(block, language))
					}
				case "resolveCallTargets":
					for _, probe := range resolutions {
						count += len(resolveCallTargets(probe.name, probe.from, byName[probe.name], records[probe.from.FilePath], nil, true))
					}
				case "forEachRelation":
					count = len(relationPass(nil))
				case "forEachRelation_precomputed_imports":
					count = len(relationPass(imports))
				}
				elapsed := time.Since(started).Nanoseconds()
				if count == 0 {
					t.Fatalf("empty %s %s probe", language, phase)
				}
				observation := map[string]any{"language": language, "files": len(snapshot.Files), "symbols": len(snapshot.Symbols), "phase": phase, "trial": trial, "elapsed_ns": elapsed, "result_count": count, "corpus_sha256": digest, "profile": "full", "workers": 1, "raw_import_equivalence": true}
				if err := encoder.Encode(observation); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
