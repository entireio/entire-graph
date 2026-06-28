package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/suhaanthayyil/entire-sem/internal/sem"
	"github.com/suhaanthayyil/entire-sem/internal/sem/lsp"
)

// runLSPCalls drives the optional LSP call-resolution path (the parallel,
// higher-fidelity resolver) and emits its resolved call graph as JSON. With
// --diff it also runs the tree-sitter heuristic and reports where the two
// diverge — the divergence set is a diagnostic for finding heuristic gaps
// (recall: lsp_only) and over-emission (precision: heuristic_only).
func runLSPCalls(ctx context.Context, opts Options, args []string) error {
	var repo, language string
	diff := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repo":
			i++
			if i >= len(args) {
				return errors.New("--repo requires a value")
			}
			repo = args[i]
		case "--language":
			i++
			if i >= len(args) {
				return errors.New("--language requires a value")
			}
			language = args[i]
		case "--diff":
			diff = true
		default:
			return fmt.Errorf("lsp-calls: unexpected argument %q", args[i])
		}
	}
	if language == "" {
		return errors.New("lsp-calls requires --language")
	}
	resolvedRepo, err := resolveRepo(ctx, opts.Env, repo)
	if err != nil {
		return err
	}

	res, ok, err := lsp.ResolveCalls(ctx, resolvedRepo, language)
	if err != nil {
		return fmt.Errorf("lsp resolve: %w", err)
	}
	if !ok {
		return fmt.Errorf("no LSP server available for language %q", language)
	}

	enc := json.NewEncoder(opts.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")

	if !diff {
		return enc.Encode(map[string]any{
			"repo":     resolvedRepo,
			"language": language,
			"symbols":  res.Symbols,
			"calls":    res.Calls,
			"stats": map[string]int{
				"symbols": len(res.Symbols), "calls": len(res.Calls),
				"load_errors": res.LoadErrors, "stalled": res.Stalled,
			},
		})
	}

	heuristic, err := heuristicCallEdges(ctx, resolvedRepo, opts.Version)
	if err != nil {
		return fmt.Errorf("heuristic snapshot: %w", err)
	}
	lspSet := map[edgeKey]lsp.CallEdge{}
	for _, e := range res.Calls {
		lspSet[edgeKey{e.FromFile, e.FromLine, e.ToFile, e.ToLine}] = e
	}
	var lspOnly, heuristicOnly []lsp.CallEdge
	shared := 0
	for k, e := range lspSet {
		if _, ok := heuristic[k]; ok {
			shared++
		} else {
			lspOnly = append(lspOnly, e)
		}
	}
	for k, e := range heuristic {
		if _, ok := lspSet[k]; !ok {
			heuristicOnly = append(heuristicOnly, e)
		}
	}
	sortEdges(lspOnly)
	sortEdges(heuristicOnly)
	return enc.Encode(map[string]any{
		"repo":     resolvedRepo,
		"language": language,
		"stats": map[string]int{
			"heuristic_calls": len(heuristic), "lsp_calls": len(lspSet),
			"shared": shared, "lsp_only": len(lspOnly), "heuristic_only": len(heuristicOnly),
		},
		// lsp_only: calls the heuristic missed (recall gaps to investigate).
		"lsp_only": lspOnly,
		// heuristic_only: calls the LSP did not confirm (likely over-emission).
		"heuristic_only": heuristicOnly,
	})
}

type edgeKey struct {
	ff string
	fl int
	tf string
	tl int
}

// heuristicCallEdges runs the tree-sitter snapshot and returns its CALLS edges
// in (file, start-line) identity, keyed for set comparison with the LSP edges.
func heuristicCallEdges(ctx context.Context, repo, version string) (map[edgeKey]lsp.CallEdge, error) {
	site := map[string][2]any{} // symbol ID -> (relfile, line)
	name := map[string]string{}
	type rawEdge struct{ from, to string }
	var raw []rawEdge
	err := sem.StreamSnapshot(ctx, repo, version, sem.ProviderSnapshotOptions{Profile: sem.ProfileFull}, func(record any) error {
		switch r := record.(type) {
		case sem.SymbolRecord:
			site[r.ID] = [2]any{relPath(repo, r.FilePath), r.StartLine}
			name[r.ID] = r.Name
		case sem.RelationRecord:
			if r.Type == "CALLS" {
				raw = append(raw, rawEdge{r.FromID, r.ToID})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := map[edgeKey]lsp.CallEdge{}
	for _, e := range raw {
		f, okF := site[e.from]
		t, okT := site[e.to]
		if !okF || !okT {
			continue
		}
		k := edgeKey{f[0].(string), f[1].(int), t[0].(string), t[1].(int)}
		out[k] = lsp.CallEdge{
			FromFile: k.ff, FromLine: k.fl, ToFile: k.tf, ToLine: k.tl,
			FromName: name[e.from], ToName: name[e.to],
		}
	}
	return out, nil
}

func relPath(repo, file string) string {
	if r, err := filepath.Rel(repo, file); err == nil {
		return filepath.ToSlash(r)
	}
	return file
}

func sortEdges(edges []lsp.CallEdge) {
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.FromFile != b.FromFile {
			return a.FromFile < b.FromFile
		}
		if a.FromLine != b.FromLine {
			return a.FromLine < b.FromLine
		}
		if a.ToFile != b.ToFile {
			return a.ToFile < b.ToFile
		}
		return a.ToLine < b.ToLine
	})
}
