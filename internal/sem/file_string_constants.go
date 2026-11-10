package sem

import "sync"

// fileStringConstants derives each file's static string constants once for the
// whole relation phase.
//
// Two passes over the same files want them. The HANDLES_ROUTE pass runs first
// and reads them to resolve a route spelled as a constant; the per-file loop
// reads them again for the same reason. Each derived its own map from the same
// content, and the derivation is a regex scan of the whole file repeated until
// it reaches a fixed point, so for a Go repository it was the second most
// expensive thing the relation phase did after the flow scanners.
//
// The lock makes it safe for the per-file loop's workers. It is taken once per
// file, not once per lookup within a file, so it does not serialize them.
type fileStringConstants struct {
	mu sync.Mutex
	by map[string]map[string]string
}

func newFileStringConstants() *fileStringConstants {
	return &fileStringConstants{by: map[string]map[string]string{}}
}

// forFile returns path's constants, deriving them from content on first ask.
//
// The returned map is shared with every other caller for the same path and must
// not be mutated. Its consumers read it to expand a constant reference.
func (c *fileStringConstants) forFile(path, content string) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if constants, ok := c.by[path]; ok {
		return constants
	}
	constants := staticStringConstants(content)
	c.by[path] = constants
	return constants
}
