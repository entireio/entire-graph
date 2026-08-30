//go:build windows

package sem

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGitMetadataConfigGuardRejectsCoreWorktreeUNCBeforeLookup(t *testing.T) {
	resolver, err := newSameVolumePathResolver(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()

	// The documentation-only TEST-NET address must never be contacted. A UNC
	// share is a different Windows volume, so the rooted resolver rejects its
	// volume spelling before issuing the first metadata lookup.
	if gitCoreWorktreePathSafeWithResolver(resolver, resolver.baseResolved, `\\192.0.2.1\entire-graph\worktree`) {
		t.Fatal("UNC core.worktree passed metadata preflight")
	}
}

func TestGitMetadataConfigGuardRejectsPromisorUNCBeforeLookup(t *testing.T) {
	cases := []struct {
		name   string
		marker string
	}{
		{
			name:   "partial clone extension",
			marker: "[extensions]\npartialClone = origin\n",
		},
		{
			name:   "independent promisor remote",
			marker: "[remote \"origin\"]\npromisor = true\n",
		},
		{
			name:   "partial clone filter alone",
			marker: "[remote \"origin\"]\npartialCloneFilter = blob:none\n",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repo, gitDir := gitConfigPreflightFixture(t)
			content := tt.marker + "[remote \"origin\"]\nurl = \\\\192.0.2.1\\entire-graph\\promisor.bundle\n"
			if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}

			done := make(chan bool, 1)
			go func() { done <- gitMetadataSafeForSubprocess(repo) }()
			select {
			case safe := <-done:
				if safe {
					t.Fatalf("promisor configuration naming a UNC URL passed metadata preflight:\n%s", content)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("promisor config preflight did not return promptly; the UNC URL must remain inert config text")
			}
		})
	}
}
