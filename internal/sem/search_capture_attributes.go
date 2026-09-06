package sem

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
		observed, err := gitutil.CapturedDiffAttributes(ctx, source.absRepo, missing, func(path string) (string, bool, error) {
			content, present, err := capturePolicyRead(store, source.absRepo, path, func() (string, bool, error) { content, ok := store.read(path); return content, ok, nil })
			if !present && source.oversize != nil {
				if _, oversized := source.oversize(path); oversized {
					return "", false, fmt.Errorf("attribute policy exceeds the captured read limit: %s", path)
				}
			}
			if !present && err == nil {
				info, statErr := os.Lstat(filepath.Join(source.absRepo, filepath.FromSlash(path)))
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
		for path, attribute := range observed {
			if _, exists := store.attributeDecisions[path]; !exists {
				store.attributeDecisions[path] = attribute
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
