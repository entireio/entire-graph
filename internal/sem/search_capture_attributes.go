package sem

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/entireio/entire-graph/internal/gitutil"
)

func (source sourceContext) capturedDiffAttributes(ctx context.Context, paths []string) (map[string]gitutil.CapturedDiffAttribute, error) {
	store := source.capture
	if store == nil {
		return nil, fmt.Errorf("attribute policy requires an operation capture")
	}
	store.mu.Lock()
	missing := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, ok := store.attributeDecisions[path]; !ok {
			missing = append(missing, path)
		}
	}
	store.mu.Unlock()
	if len(missing) > 0 {
		if err := EnsureGitMetadataSafeForSubprocess(source.absRepo); err != nil {
			return nil, err
		}
		gitRoot, err := gitutil.RepoRoot(ctx, source.absRepo)
		if err != nil {
			return nil, err
		}
		prefix, err := gitutil.RepoPrefix(ctx, source.absRepo)
		if err != nil {
			return nil, err
		}
		rootPaths := make([]string, len(missing))
		for index, path := range missing {
			rootPaths[index] = prefix + path
		}
		root, err := os.OpenRoot(gitRoot)
		if err != nil {
			return nil, fmt.Errorf("open captured Git root: %w", err)
		}
		defer root.Close()
		registry := newOversizeRegistry(root, gitRoot)
		rootRead := capturedWorktreeReader(ctx, root, gitRoot, int64(resolveMaxParseBytes(0)), registry, nil)
		observed, err := gitutil.CapturedDiffAttributes(ctx, gitRoot, rootPaths, func(path string) (string, bool, error) {
			absolute := filepath.Join(gitRoot, filepath.FromSlash(path))
			withinSelected := prefix == "" || strings.HasPrefix(path, prefix)
			selected := path
			if withinSelected && prefix != "" {
				selected = strings.TrimPrefix(path, prefix)
			}
			content, present, err := capturePolicyRead(store, source.absRepo, absolute, func() (string, bool, error) {
				if withinSelected {
					content, ok := store.read(selected)
					return content, ok, nil
				}
				content, ok := rootRead(path)
				return content, ok, nil
			})
			if !present && source.oversize != nil {
				if _, oversized := source.oversize(selected); oversized {
					return "", false, fmt.Errorf("attribute policy exceeds the captured read limit: %s", path)
				}
			}
			if !present {
				if _, oversized := registry.lookup(path); oversized {
					return "", false, fmt.Errorf("attribute policy exceeds the captured read limit: %s", path)
				}
			}
			if !present && err == nil {
				info, statErr := os.Lstat(absolute)
				if statErr != nil && !os.IsNotExist(statErr) {
					return "", false, fmt.Errorf("attribute policy unavailable: %s: %w", path, statErr)
				}
				if statErr == nil && info.Mode()&os.ModeSymlink == 0 {
					return "", false, fmt.Errorf("attribute policy exists but could not be captured: %s", path)
				}
			}
			return content, present, err
		})
		if err != nil {
			return nil, err
		}
		store.mu.Lock()
		if store.attributeDecisions == nil {
			store.attributeDecisions = map[string]gitutil.CapturedDiffAttribute{}
		}
		// Concurrent consumers cannot replace the first retained decision.
		for index, path := range missing {
			rootPath := rootPaths[index]
			if attribute, exists := observed[rootPath]; exists {
				if _, retained := store.attributeDecisions[path]; !retained {
					store.attributeDecisions[path] = attribute
				}
			}
		}
		encoded, err := json.Marshal(store.attributeDecisions)
		if err == nil {
			store.attributeDecisionDigest = contentHash(encoded)
		}
		store.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make(map[string]gitutil.CapturedDiffAttribute, len(paths))
	for _, path := range paths {
		result[path] = store.attributeDecisions[path]
	}
	return result, nil
}
