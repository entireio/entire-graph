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
			for _, dir := range []string{"pkg/credentials", "internal/secrets", "deploy/secrets"} {
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
