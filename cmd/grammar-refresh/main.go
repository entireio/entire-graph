// Command grammar-refresh vendors a tree-sitter grammar into
// internal/sem/grammars/<lang> from its upstream git repository and records
// where it came from in internal/sem/grammars/grammars.json. It is a
// maintainer tool: it needs the network, and its output is committed, so
// building entire-graph never downloads anything.
//
// Usage:
//
//	go run ./cmd/grammar-refresh -lang swift -ref 0.7.4-with-generated-files
//	go run ./cmd/grammar-refresh -lang fsharp -ref <commit> -path fsharp
//	go run ./cmd/grammar-refresh -lang newlang -ref v1.0.0 -url https://github.com/o/tree-sitter-newlang
//
// The ref must contain generated sources (src/parser.c) built for ABI 13-14,
// the range the go-tree-sitter runtime loads. binding.go is never touched, so
// a new language still needs one. A grammar that changes parse results needs
// an IdentityRevision bump (internal/sem/provider.go) in the same commit.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	var (
		lang         = flag.String("lang", "", "grammar directory name under -dir (required)")
		ref          = flag.String("ref", "", "tag, branch or full commit to vendor (required)")
		url          = flag.String("url", "", "git URL; defaults to the manifest entry")
		subdir       = flag.String("path", "", "grammar subdirectory within the repository; defaults to the manifest entry")
		manifestPath = flag.String("manifest", "internal/sem/grammars/grammars.json", "path to the grammar manifest")
		grammarsDir  = flag.String("dir", "internal/sem/grammars", "directory holding vendored grammars")
	)
	flag.Parse()
	if *lang == "" || *ref == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := refresh(*lang, *ref, *url, *subdir, *manifestPath, *grammarsDir); err != nil {
		fmt.Fprintln(os.Stderr, "grammar-refresh:", err)
		os.Exit(1)
	}
}

var languageNamePattern = regexp.MustCompile(`^[a-z0-9_]+$`)

func refresh(lang, ref, url, subdir, manifestPath, grammarsDir string) error {
	if !languageNamePattern.MatchString(lang) {
		return fmt.Errorf("invalid -lang %q: use the lower-case directory name under %s", lang, grammarsDir)
	}
	if subdir != "" && !filepath.IsLocal(subdir) {
		return fmt.Errorf("invalid -path %q: must be a relative path inside the repository", subdir)
	}
	grammars, err := loadManifest(manifestPath)
	if err != nil {
		return err
	}
	var existing Grammar
	for _, g := range grammars {
		if g.Language == lang {
			existing = g
		}
	}
	if url == "" {
		url = existing.URL
	}
	if url == "" {
		return fmt.Errorf("%s is not in %s; pass -url", lang, manifestPath)
	}
	if subdir == "" {
		subdir = existing.Path
	}
	if subdir != "" && !filepath.IsLocal(subdir) {
		return fmt.Errorf("invalid path %q for %s in %s: must be a relative path inside the repository", subdir, lang, manifestPath)
	}

	tmp, err := os.MkdirTemp("", "grammar-refresh-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := git(tmp, "init", "-q"); err != nil {
		return err
	}
	if err := git(tmp, "fetch", "-q", "--depth", "1", "--", url, ref); err != nil {
		return fmt.Errorf("fetch %s %s: %w", url, ref, err)
	}
	if err := git(tmp, "checkout", "-q", "FETCH_HEAD"); err != nil {
		return err
	}
	revision, err := gitOutput(tmp, "rev-parse", "HEAD")
	if err != nil {
		return err
	}

	files, abi, err := vendor(tmp, subdir, filepath.Join(grammarsDir, lang))
	if err != nil {
		return fmt.Errorf("%s at %s: %w", url, ref, err)
	}
	note := ""
	if existing.Revision == revision {
		note = existing.Note
	}
	grammars = upsert(grammars, Grammar{
		Language:  lang,
		URL:       url,
		Reference: ref,
		Revision:  revision,
		Path:      subdir,
		Files:     files,
		ABI:       abi,
		Note:      note,
	})
	if err := saveManifest(manifestPath, grammars); err != nil {
		return err
	}
	fmt.Printf("vendored %s %s (%s, ABI %d) into %s\n", lang, ref, revision[:7], abi, filepath.Join(grammarsDir, lang))
	fmt.Println("next: bump IdentityRevision in internal/sem/provider.go if parse results change, and run scripts/gen-notices.sh")
	return nil
}

func git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
