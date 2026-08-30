package sem

import (
	"go/build"
	"testing"
)

func TestMetadataTreeEntryBuildConstraints(t *testing.T) {
	targets := []struct {
		goos   string
		goarch string
	}{
		{goos: "aix", goarch: "ppc64"},
		{goos: "android", goarch: "arm64"},
		{goos: "darwin", goarch: "arm64"},
		{goos: "dragonfly", goarch: "amd64"},
		{goos: "freebsd", goarch: "amd64"},
		{goos: "illumos", goarch: "amd64"},
		{goos: "ios", goarch: "arm64"},
		{goos: "linux", goarch: "amd64"},
		{goos: "netbsd", goarch: "amd64"},
		{goos: "openbsd", goarch: "amd64"},
		{goos: "solaris", goarch: "amd64"},
	}

	for _, target := range targets {
		t.Run(target.goos, func(t *testing.T) {
			context := build.Default
			context.GOOS = target.goos
			context.GOARCH = target.goarch
			context.CgoEnabled = true

			posix, err := context.MatchFile(".", "metadata_tree_entry_posix.go")
			if err != nil {
				t.Fatalf("match POSIX implementation: %v", err)
			}
			unsupported, err := context.MatchFile(".", "metadata_tree_entry_unsupported.go")
			if err != nil {
				t.Fatalf("match unsupported implementation: %v", err)
			}
			if !posix || unsupported {
				t.Fatalf("cgo target selected posix=%t unsupported=%t", posix, unsupported)
			}

			context.CgoEnabled = false
			posix, err = context.MatchFile(".", "metadata_tree_entry_posix.go")
			if err != nil {
				t.Fatalf("match POSIX implementation without cgo: %v", err)
			}
			unsupported, err = context.MatchFile(".", "metadata_tree_entry_unsupported.go")
			if err != nil {
				t.Fatalf("match unsupported implementation without cgo: %v", err)
			}
			if posix || !unsupported {
				t.Fatalf("non-cgo target selected posix=%t unsupported=%t", posix, unsupported)
			}
		})
	}
}
