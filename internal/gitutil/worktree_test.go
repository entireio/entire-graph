package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func newWorktreeRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "Entire Graph Test")
	git(t, repo, "config", "user.email", "graph@example.com")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("ignored/\n*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "app.go"), []byte("package app\n\nfunc Run() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	return repo
}

// The cleanliness verdict is the precondition for reusing a tree-keyed index for
// a working-tree query, so every way the working tree can diverge from HEAD must
// report false — and a change git legitimately ignores must not.
func TestWorktreeMatchesHEAD(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(t *testing.T, repo string)
		want   bool
	}{
		{
			name:   "clean tree equals HEAD",
			mutate: func(*testing.T, string) {},
			want:   true,
		},
		{
			name: "modified tracked file",
			mutate: func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(repo, "app.go"), []byte("package app\n\nfunc Run() { println(1) }\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: false,
		},
		{
			name: "staged but uncommitted change",
			mutate: func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(repo, "staged.go"), []byte("package app\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				git(t, repo, "add", "staged.go")
			},
			want: false,
		},
		{
			name: "deleted tracked file",
			mutate: func(t *testing.T, repo string) {
				if err := os.Remove(filepath.Join(repo, "app.go")); err != nil {
					t.Fatal(err)
				}
			},
			want: false,
		},
		{
			// A working-tree walk indexes untracked-but-not-ignored files, so
			// "no tracked modifications" alone is not equality with HEAD.
			name: "untracked file",
			mutate: func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(repo, "extra.go"), []byte("package app\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: false,
		},
		{
			name: "untracked file in an untracked directory",
			mutate: func(t *testing.T, repo string) {
				if err := os.MkdirAll(filepath.Join(repo, "pkg", "deep"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, "pkg", "deep", "x.go"), []byte("package deep\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: false,
		},
		{
			// Ignored files are excluded from the index walk too, so they cannot
			// invalidate equality.
			name: "ignored file appears",
			mutate: func(t *testing.T, repo string) {
				if err := os.MkdirAll(filepath.Join(repo, "ignored"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, "ignored", "build.go"), []byte("package ignored\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, "debug.log"), []byte("noise\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repo := newWorktreeRepo(t)
			testCase.mutate(t, repo)
			got, err := WorktreeMatchesHEAD(context.Background(), repo)
			if err != nil {
				t.Fatalf("WorktreeMatchesHEAD error = %v", err)
			}
			if got != testCase.want {
				t.Fatalf("WorktreeMatchesHEAD = %v, want %v", got, testCase.want)
			}
		})
	}
}

// A repository with no HEAD has no committed tree to be equal to, and a
// non-repository is not a repository. Both must error rather than report true:
// a hopeful true would serve a cache entry for content nothing verified.
func TestWorktreeMatchesHEADErrorsWithoutCommittedHEAD(t *testing.T) {
	t.Parallel()
	empty := t.TempDir()
	git(t, empty, "init")
	if clean, err := WorktreeMatchesHEAD(context.Background(), empty); err == nil || clean {
		t.Fatalf("empty repo = (%v, %v), want (false, error)", clean, err)
	}
	plain := t.TempDir()
	if clean, err := WorktreeMatchesHEAD(context.Background(), plain); err == nil || clean {
		t.Fatalf("non-repo = (%v, %v), want (false, error)", clean, err)
	}
}

// WorktreeDirtyPaths is the factual half of the cleanliness question: it must
// name every path that diverges, so a caller can decide which of them it
// actually reads. A rename spends two records in porcelain -z output and must
// yield BOTH paths, not a phantom entry parsed out of the source field.
func TestWorktreeDirtyPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(t *testing.T, repo string)
		want   []string
	}{
		{
			name:   "clean tree reports nothing",
			mutate: func(*testing.T, string) {},
			want:   nil,
		},
		{
			name: "untracked file is reported",
			mutate: func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(repo, ".DS_Store"), []byte("\x00"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{".DS_Store"},
		},
		{
			name: "modified tracked file is reported",
			mutate: func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(repo, "app.go"), []byte("package app\n\nfunc Run() { println(1) }\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"app.go"},
		},
		{
			name: "rename reports both destination and source",
			mutate: func(t *testing.T, repo string) {
				git(t, repo, "mv", "app.go", "moved.go")
			},
			want: []string{"app.go", "moved.go"},
		},
		{
			name: "ignored file is not reported",
			mutate: func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(repo, "build.log"), []byte("noise\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := newWorktreeRepo(t)
			tc.mutate(t, repo)
			got, err := WorktreeDirtyPaths(context.Background(), repo)
			if err != nil {
				t.Fatalf("WorktreeDirtyPaths: %v", err)
			}
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("paths = %q, want %q", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("paths = %q, want %q", got, want)
				}
			}
			// The bool wrapper must stay exactly len(paths)==0.
			clean, err := WorktreeMatchesHEAD(context.Background(), repo)
			if err != nil {
				t.Fatalf("WorktreeMatchesHEAD: %v", err)
			}
			if clean != (len(got) == 0) {
				t.Fatalf("WorktreeMatchesHEAD = %v with dirty paths %q", clean, got)
			}
		})
	}
}
