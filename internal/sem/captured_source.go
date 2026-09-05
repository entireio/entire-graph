package sem

// capturedSource is one successful bounded acquisition, not an atomic repository
// revision. Strings keep the bytes immutable across extraction and assembly.
// The caller retains responsibility for path confinement and read failures.
// This initial seam does not retain sources across provider phases.
type capturedSource struct {
	path    string
	content string
	digest  string
}

func captureSource(path, content string) capturedSource {
	return capturedSource{path: path, content: content, digest: contentHash([]byte(content))}
}
