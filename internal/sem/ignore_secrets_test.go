package sem

import (
	"os"
	"path/filepath"
	"testing"
)

// credentialStoreDenied are paths the built-in deny must cover. Every one of them
// is a file whose CONTENT is the secret.
var credentialStoreDenied = []string{
	".env", ".env.local", ".env.production.local", "config/.env", ".env.example",
	"deploy/prod.env", ".ENV", ".envrc",
	".npmrc", "sub/dir/.npmrc", ".netrc", "_netrc", ".pgpass", ".htpasswd",
	".pypirc", ".dockercfg", ".boto",
	"id_rsa", "id_dsa", "id_ecdsa", "deploy/.ssh/id_ed25519",
	".aws/credentials", "credentials.json", "config/credentials.yml", "secrets.yaml",
	// Exact, tool-owned credential stores. Exercise root, nested, and case variants:
	// the rules must match the store file at any depth without admitting a
	// similarly named directory.
	".git-credentials", "nested/.git-credentials", ".GIT-CREDENTIALS",
	".docker/config.json", "clusters/prod/.docker/config.json", ".DOCKER/CONFIG.JSON",
	".kube/config", "clusters/prod/.kube/config", ".KUBE/CONFIG",
	"credentials.tfrc.json", "nested/credentials.tfrc.json", "CREDENTIALS.TFRC.JSON",
	"application_default_credentials.json", "nested/application_default_credentials.json",
	"APPLICATION_DEFAULT_CREDENTIALS.JSON",
	"certs/server.pem", "keys/private.key", "store.jks", "bundle.p12", "cert.pfx",
	"app.keystore", "ca.truststore", "backup.kdbx", "release.asc", "signing.gpg",
	"server.ppk", "vault.pkcs12",
	// Path-shaped: the basename alone says nothing, only the directory segment does.
	"deploy/secrets/prod-secrets.yaml", "secrets/api.json", "k8s/base/secrets/db.yml",
	"infra/credentials/aws.toml", "secrets/tokens.txt",
}

// credentialStoreAllowed are paths the deny must NOT cover. Source code that reads
// or manages credentials is the legitimate answer to a credentials query, and the
// public half of a key pair is published on purpose.
var credentialStoreAllowed = []string{
	"internal/config/dotenv.go", "src/env.ts", "lib/environment.py",
	"pkg/credentials/provider.go", "internal/secrets/manager.go",
	"deploy/secrets/rotate.sh", "infra/credentials/main.tf",
	"docs/secrets.md", "cmd/keygen/main.go", "environment.yml", "envoy.yaml",
	"src/secret_manager.rs", "keyring.c", "k8s/issuer-secrets.yaml",
	"certs/server.crt", "certs/ca.cer", "id_rsa.pub", "package.json",
	".git-credentials/reader.go", "nested/.git-credentials/reader.go",
	".docker/client.go", "clusters/prod/.docker/client.go", ".docker/config.json/reader.go",
	".kube/client.go", "clusters/prod/.kube/client.go", ".kube/config/reader.go",
	"credentials.tfrc.json/reader.go", "nested/credentials.tfrc.json/reader.go",
	"application_default_credentials.json/reader.go",
}

// TestBuiltinCredentialStoreDenyCoversBothCorpora pins the taxonomy directly, on
// both loaders, so a later edit cannot silently narrow it.
//
// Both loaders matter and they are not the same code path: the working-tree
// listing is filtered by loadWorktreeIgnoreMatcher and the committed-tree
// (`--head`) listing by loadExplicitIgnoreMatcher, which does not read .gitignore
// at all — so before this change a COMMITTED `.env` had no exclusion whatsoever on
// the `--head` path even in a repository whose .gitignore names it.
func TestBuiltinCredentialStoreDenyCoversBothCorpora(t *testing.T) {
	t.Parallel()

	for _, loader := range []struct {
		name string
		load func(string, []string, []string) (ignoreMatcher, error)
	}{
		{name: "worktree", load: loadWorktreeIgnoreMatcher},
		{name: "head", load: loadExplicitIgnoreMatcher},
	} {
		t.Run(loader.name, func(t *testing.T) {
			t.Parallel()
			// A repository with no exclude files of its own: the only rules in play are
			// the built-in ones.
			matcher, err := loader.load(t.TempDir(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range credentialStoreDenied {
				if !matcher.Ignored(path, false) {
					t.Errorf("%q is not excluded; a credential store must never enter the corpus", path)
				}
			}
			for _, path := range credentialStoreAllowed {
				if matcher.Ignored(path, false) {
					t.Errorf("%q is excluded; the deny must not swallow source, prose or public keys", path)
				}
			}
			// A source package under a directory segment the deny names must stay
			// walkable, or every file below it disappears with it.
			for _, dir := range []string{
				"pkg/credentials", "internal/secrets", "deploy/secrets",
				".git-credentials", "nested/.git-credentials",
				".docker", "clusters/prod/.docker", ".docker/config.json",
				".kube", "clusters/prod/.kube", ".kube/config",
				"credentials.tfrc.json", "nested/credentials.tfrc.json",
				"application_default_credentials.json",
			} {
				if matcher.Ignored(dir, true) {
					t.Errorf("directory %q is excluded; the directory rules are scoped to data and "+
						"config suffixes precisely so a source package survives", dir)
				}
			}
		})
	}
}

// TestBuiltinCredentialStoreDenyIsOverriddenByIncludeFile pins the documented
// escape hatch. The built-in rules are loaded BEFORE the caller's explicit files
// and the later rule wins, which is the same ordering .graphignore already relies
// on.
func TestBuiltinCredentialStoreDenyIsOverriddenByIncludeFile(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	includeFile := filepath.Join(t.TempDir(), "include.txt")
	if err := os.WriteFile(includeFile, []byte(".env\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	without, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !without.Ignored(".env", false) {
		t.Fatal(".env is not excluded by default; the override test below would prove nothing")
	}

	with, err := loadWorktreeIgnoreMatcher(repo, nil, []string{includeFile})
	if err != nil {
		t.Fatal(err)
	}
	if with.Ignored(".env", false) {
		t.Error("--include-file did not re-admit .env; the built-in deny must stay overridable")
	}
	if !with.Reincluded(".env", false) {
		t.Error("--include-file did not mark .env re-included; a gitignored credential store " +
			"would never reach the listing to be re-admitted")
	}
	// The override is scoped to what the caller named, not to the whole class.
	if !with.Ignored(".npmrc", false) {
		t.Error("--include-file naming .env also re-admitted .npmrc")
	}
}

// TestBuiltinCredentialStoreDenyOutranksRepositoryNegation pins the other half of
// the ordering: the built-in rules are loaded AFTER the repository's own exclude
// files, so a negation shipped INSIDE the repository under analysis cannot switch
// the deny off. The repository is the untrusted input here; the command line is
// not.
func TestBuiltinCredentialStoreDenyOutranksRepositoryNegation(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("!.env\n!*.pem\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, graphIgnoreFileName), []byte("!credentials.json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	matcher, err := loadWorktreeIgnoreMatcher(repo, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".env", "certs/server.pem", "credentials.json"} {
		if !matcher.Ignored(path, false) {
			t.Errorf("a negation inside the repository re-admitted %q; the built-in deny must "+
				"outrank .gitignore and .graphignore", path)
		}
	}
}

// TestBuiltinCredentialStoreDenyKeepsRepositoryDirectoryExclusions pins the other
// direction of the same precedence rule: the built-in deny outranks a repository
// negation, but it must never itself act as a negation of a repository EXCLUSION.
//
// The bare `credentials` entry names the AWS CLI shape (.aws/credentials). A bare
// gitignore pattern matches every path segment, not only the basename, so that one
// entry also covered a directory named credentials/ and everything under it. The
// first attempt to carve that back out used a `!credentials/` negation — and
// because the built-in block is loaded AFTER .gitignore/.graphignore precisely so
// it can outrank them, that negation re-admitted every file under any directory a
// repository had excluded as `credentials/`. The correct shape is file-only
// matching for the bare entry: it never matches a directory and never matches an
// ancestor segment, so it neither swallows nor re-admits a directory.
func TestBuiltinCredentialStoreDenyKeepsRepositoryDirectoryExclusions(t *testing.T) {
	t.Parallel()

	for _, loader := range []struct {
		name       string
		ignoreFile string
		load       func(string, []string, []string) (ignoreMatcher, error)
	}{
		// loadExplicitIgnoreMatcher (the --head corpus) reads .graphignore only.
		{name: "worktree", ignoreFile: ".gitignore", load: loadWorktreeIgnoreMatcher},
		{name: "head", ignoreFile: graphIgnoreFileName, load: loadExplicitIgnoreMatcher},
	} {
		t.Run(loader.name, func(t *testing.T) {
			t.Parallel()

			repo := t.TempDir()
			if err := os.WriteFile(filepath.Join(repo, loader.ignoreFile),
				[]byte("credentials/\nvendor/secrets/\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			matcher, err := loader.load(repo, nil, nil)
			if err != nil {
				t.Fatal(err)
			}

			for _, path := range []string{
				"credentials/private.go", "credentials/nested/deep.go",
				"vendor/secrets/loader.go",
			} {
				if !matcher.Ignored(path, false) {
					t.Errorf("the built-in deny re-admitted %q, which the repository excluded "+
						"by directory; the built-in block must never negate a repository exclusion", path)
				}
			}
			if !matcher.Ignored("credentials", true) {
				t.Error(`the built-in deny re-admitted the directory "credentials", which the repository excluded`)
			}

			// The guarantees the built-in entry exists for must survive unchanged.
			for _, path := range []string{".aws/credentials", "credentials"} {
				if !matcher.Ignored(path, false) {
					t.Errorf("the credential store %q is no longer denied", path)
				}
			}
		})
	}
}

// TestBuiltinCredentialStoreDenyLeavesSourcePackagesSearchable is the kind-(b)
// guard on the same entry: with no repository exclusion in play, a SOURCE package
// directory named credentials/ must stay walked and its .go files searchable,
// while a file literally named credentials inside it is still a credential store.
func TestBuiltinCredentialStoreDenyLeavesSourcePackagesSearchable(t *testing.T) {
	t.Parallel()

	for _, loader := range []struct {
		name string
		load func(string, []string, []string) (ignoreMatcher, error)
	}{
		{name: "worktree", load: loadWorktreeIgnoreMatcher},
		{name: "head", load: loadExplicitIgnoreMatcher},
	} {
		t.Run(loader.name, func(t *testing.T) {
			t.Parallel()

			matcher, err := loader.load(t.TempDir(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"pkg/credentials/provider.go", "credentials/provider.go"} {
				if matcher.Ignored(path, false) {
					t.Errorf("source file %q was denied; the bare credentials entry must be file-only", path)
				}
			}
			if matcher.Ignored("pkg/credentials", true) {
				t.Error("the source package directory pkg/credentials was denied")
			}
			if !matcher.Ignored("pkg/credentials/credentials", false) {
				t.Error("a file named credentials inside a source package is still a credential store")
			}
		})
	}
}

// TestBuiltinSecretFileOnlyPatternsAreAllPresent pins the file-only marking to the
// pattern block: an edit that renames or drops one of these entries would otherwise
// leave builtinSecretFileOnlyPatterns naming nothing and silently restore a rule
// that covers a matching directory tree.
func TestBuiltinSecretFileOnlyPatternsAreAllPresent(t *testing.T) {
	t.Parallel()

	if len(builtinSecretFileOnlyPatterns) == 0 {
		t.Fatal("builtinSecretFileOnlyPatterns is empty; credential-store file rules must be file-only")
	}
	marked := map[string]bool{}
	for _, rule := range builtinSecretIgnoreRules {
		if rule.fileOnly {
			marked[rule.pattern] = true
		}
	}
	for pattern := range builtinSecretFileOnlyPatterns {
		if !marked[pattern] {
			t.Errorf("built-in pattern %q is declared file-only but no parsed rule carries it; "+
				"it is missing from builtinSecretIgnorePatterns", pattern)
		}
	}
}
