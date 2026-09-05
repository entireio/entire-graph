package sem

import "path/filepath"

// Policy readers retain their existing authorization and bounds. Their observed
// bytes share the operation store with later source/resolver/snippet consumers.
func capturePolicyRead(store *capturedStore, repo, path string, read func() (string, bool, error)) (string, bool, error) {
	if store == nil {
		return read()
	}
	key := path
	if filepath.IsAbs(path) {
		relative, err := filepath.Rel(repo, path)
		// Git indirection validation may return a resolved root spelling (for
		// example /private/var on Darwin). Keep same-root policy/source keys equal.
		if err != nil || !filepath.IsLocal(relative) {
			if resolved, resolveErr := filepath.EvalSymlinks(repo); resolveErr == nil {
				relative, err = filepath.Rel(resolved, path)
			}
		}
		if err == nil && filepath.IsLocal(relative) {
			key = relative
		} else {
			key = "external-policy:" + path
		}
	}
	key = filepath.ToSlash(key)
	var readErr error
	source, present, err := store.acquireFrom(key, func(string) (string, bool) {
		content, ok, loadErr := read()
		readErr = loadErr
		return content, ok && loadErr == nil
	})
	if err != nil {
		return "", false, err
	}
	store.mu.Lock()
	if entry := store.entries[key]; entry != nil {
		entry.policy = true
	}
	if readErr != nil && store.failure == nil {
		store.failure = readErr
	}
	store.mu.Unlock()
	return source.content, present, readErr
}
