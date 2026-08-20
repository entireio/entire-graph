package sem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Placeholder credential VALUES. The variable names AROUND them
// (STRIPE_SECRET_KEY, AWS_SECRET_ACCESS_KEY, _authToken) are what make a real
// .env/.npmrc match a query like "api key credentials token", so the fixtures
// keep those and use values that deliberately match no provider's key format: a
// fixture shaped like a live Stripe/AWS/npm key trips push protection and secret
// scanners in every repository that vendors this test.
const (
	testFakeStripeKey          = "placeholder-stripe-value-not-a-key"
	testFakeNPMToken           = "placeholder-npm-value-not-a-token"
	testFakeAWSSecret          = "placeholder-aws-value-not-a-secret"
	testFakeK8sSecret          = "placeholder-kubernetes-value-not-a-secret"
	testFakeGitCredential      = "placeholder-git-credential-not-a-token"
	testFakeDockerAuth         = "placeholder-docker-auth-not-a-token"
	testFakeKubeToken          = "placeholder-kubernetes-token-not-a-token"
	testFakeTerraformToken     = "placeholder-terraform-token-not-a-token"
	testFakeGoogleRefreshToken = "placeholder-google-refresh-token-not-a-token"
	// Deliberately not PEM-decodable: a fixture that parses as a real private key
	// trips secret scanners in every repository that vendors this test.
	testFakePrivateKey = "placeholder-private-key-material-not-a-key"
)

// credentialStoreVictimPaths are the paths no payload may name.
//
// The last one is the reason the exclusion is not a basename table:
// `prod-secrets.yaml` is an ordinary-looking filename and only its `secrets/`
// directory segment says what it holds.
var credentialStoreVictimPaths = []string{
	".env", ".npmrc", "certs/server.pem", "credentials.json",
	"deploy/secrets/prod-secrets.yaml",
	".git-credentials", ".docker/config.json", ".kube/config",
	"credentials.tfrc.json", "application_default_credentials.json",
}

// credentialStoreNeedleVictimPaths are the stores containing the literal used by
// the git-grep regression path. The other stores remain fixture victims, but do
// not contain that particular literal and therefore cannot prove the grep filter.
var credentialStoreNeedleVictimPaths = []string{
	".env", "deploy/secrets/prod-secrets.yaml",
	".git-credentials", ".docker/config.json", ".kube/config",
	"credentials.tfrc.json", "application_default_credentials.json",
}

// credentialStoreSecrets are the values that must not reach the caller through
// any field of any payload.
var credentialStoreSecrets = []string{
	testFakeStripeKey, testFakeNPMToken, testFakeAWSSecret,
	testFakePrivateKey, testFakeK8sSecret, testFakeGitCredential,
	testFakeDockerAuth, testFakeKubeToken, testFakeTerraformToken,
	testFakeGoogleRefreshToken,
}

// credentialStoreControlPaths are first-party source files that must stay fully
// searchable. Two of them sit under directory segments the exclusion also names
// (`internal/secrets/`, `pkg/credentials/`), which is what proves the
// directory rules are scoped to data and config suffixes rather than to the
// directory wholesale.
var credentialStoreControlPaths = []string{
	"internal/config/dotenv.go", "internal/http/client.go",
	"internal/secrets/manager.go", "pkg/credentials/provider.go",
}

// credentialStoreRepo writes a repository holding the canonical credential-store
// shapes plus first-party source that READS them.
//
// The source files are the positive control for the whole suite: the fix must
// remove the credential FILES without removing the code that loads credentials,
// which is what a caller searching "api key credentials token" actually wants.
//
// Every filename here is legal on Windows — no reserved device name, no
// character Windows forbids in a path, no symlink — so no runtime.GOOS guard is
// needed.
func credentialStoreRepo(t *testing.T) string {
	t.Helper()

	repo := t.TempDir()
	initRepo(t, repo)
	writeFile(t, repo, ".env", "STRIPE_SECRET_KEY="+testFakeStripeKey+"\n"+
		"DATABASE_URL=postgres://user:placeholder-password@db.internal:5432/prod\n"+
		"AWS_SECRET_ACCESS_KEY="+testFakeAWSSecret+"\n")
	writeFile(t, repo, ".npmrc", "//registry.npmjs.org/:_authToken="+testFakeNPMToken+"\n"+
		"@acme:registry=https://npm.internal.acme.com/\n")
	writeFile(t, repo, "certs/server.pem", "-----BEGIN PRIVATE KEY-----\n"+
		testFakePrivateKey+"\n-----END PRIVATE KEY-----\n")
	writeFile(t, repo, "credentials.json", "{\n  \"type\": \"service_account\",\n"+
		"  \"private_key\": \""+testFakePrivateKey+"\",\n"+
		"  \"client_email\": \"loader@acme.iam.gserviceaccount.com\"\n}\n")
	writeFile(t, repo, "deploy/secrets/prod-secrets.yaml", "apiVersion: v1\nkind: Secret\n"+
		"metadata:\n  name: prod-api-credentials\nstringData:\n"+
		"  STRIPE_SECRET_KEY: "+testFakeK8sSecret+"\n")
	writeFile(t, repo, ".git-credentials", "https://oauth2:STRIPE_SECRET_KEY."+testFakeGitCredential+
		"@git.example.internal\n")
	writeFile(t, repo, ".docker/config.json", "{\n  \"auths\": {\n"+
		"    \"registry.example.internal\": {\"auth\": \"STRIPE_SECRET_KEY."+testFakeDockerAuth+"\"}\n"+
		"  }\n}\n")
	writeFile(t, repo, ".kube/config", "apiVersion: v1\nusers:\n- name: production\n  user:\n"+
		"    token: STRIPE_SECRET_KEY."+testFakeKubeToken+"\n")
	writeFile(t, repo, "credentials.tfrc.json", "{\n  \"credentials\": {\n"+
		"    \"app.terraform.io\": {\"token\": \"STRIPE_SECRET_KEY."+testFakeTerraformToken+"\"}\n"+
		"  }\n}\n")
	writeFile(t, repo, "application_default_credentials.json", "{\n"+
		"  \"type\": \"authorized_user\",\n  \"refresh_token\": \"STRIPE_SECRET_KEY."+testFakeGoogleRefreshToken+"\"\n}\n")

	// The legitimate search targets. A fix that also sinks these has broken search
	// rather than secured it. The two literal mentions of STRIPE_SECRET_KEY below
	// are what the literal-cluster test asserts against.
	writeFile(t, repo, "internal/config/dotenv.go", `package config

import "os"

// LoadDotenvCredentials reads the api key, token and credentials config from the
// dotenv file and returns them for the http client.
func LoadDotenvCredentials() (string, string) {
	return os.Getenv("STRIPE_SECRET_KEY"), os.Getenv("AWS_SECRET_ACCESS_KEY")
}
`)
	writeFile(t, repo, "internal/http/client.go", `package http

// NewCredentialedClient builds the api client from the stripe secret key
// credentials token loaded out of the environment.
func NewCredentialedClient(env map[string]string) string {
	return env["STRIPE_SECRET_KEY"]
}
`)
	// Source under directory segments the exclusion names. These stay searchable:
	// the directory rules require a data or config suffix.
	writeFile(t, repo, "internal/secrets/manager.go", `package secrets

import "os"

// RotateApiCredentials rotates the stored api key credentials token.
func RotateApiCredentials(name string) string {
	return name + os.Getenv("STRIPE_SECRET_KEY")
}
`)
	writeFile(t, repo, "pkg/credentials/provider.go", `package credentials

import "os"

// ApiKeyCredentialsProvider resolves the api key credentials token for a request.
func ApiKeyCredentialsProvider(profile string) string {
	return profile + os.Getenv("STRIPE_SECRET_KEY")
}
`)
	// The EDIT site for the literal-cluster test: a package-level constant naming
	// STRIPE_SECRET_KEY, plus consumers of it. The block refuses to exist without a
	// live EDIT site and without a site the ranked payload did not already print,
	// so the fixture supplies both.
	writeFile(t, repo, "internal/config/keys.go", `package config

// StripeSecretKeyEnv names the api key credentials token variable.
const StripeSecretKeyEnv = "STRIPE_SECRET_KEY"
`)
	for _, name := range []string{"billing", "payouts", "refunds", "invoices"} {
		writeFile(t, repo, "internal/"+name+"/service.go", `package `+name+`

import "os"

// Charge reads the api key credentials token for the request.
func Charge(amount int) string {
	return os.Getenv("STRIPE_SECRET_KEY")
}
`)
	}

	// The public half of the key pair. Publishing it is its purpose, so it must
	// NOT be excluded.
	writeFile(t, repo, "certs/server.crt", "-----BEGIN CERTIFICATE-----\nplaceholder-public-certificate\n-----END CERTIFICATE-----\n")

	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "victim repo")
	return repo
}

// writeCredentialIncludeFile writes an --include-file OUTSIDE the repository, which
// is how a caller supplies one: an include file inside the tree would itself be part
// of the corpus.
func writeCredentialIncludeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "include.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func searchResultPaths(results []SearchResult) []string {
	paths := make([]string, 0, len(results))
	for _, result := range results {
		paths = append(paths, result.FilePath)
	}
	return paths
}

func assertNoCredentialMaterial(t *testing.T, where, text string) {
	t.Helper()
	for _, secret := range credentialStoreSecrets {
		if strings.Contains(text, secret) {
			t.Errorf("%s quoted credential material verbatim", where)
		}
	}
}

// TestCredentialStoresAreAbsentFromSearchResults is the regression guard for the
// leak, and it asserts ABSENCE FROM THE RANKED PAYLOAD rather than absence of a
// language label.
//
// The distinction is the whole bug. `.env` and `.npmrc` were once allow-listed in
// inventoryLanguageFilenames, and dropping them from that table only stops
// languageForPath from LABELLING them: the corpus is listed by the provider from
// every walked path and never consults languageForPath, so the files were still
// read, still ranked and still quoted verbatim into the calling agent's context.
// `.env.local` was never in that table at all and leaked exactly the same way.
func TestCredentialStoresAreAbsentFromSearchResults(t *testing.T) {
	t.Parallel()

	repo := credentialStoreRepo(t)
	response, err := SearchRepository(t.Context(), repo, "test-version",
		"api key credentials token",
		SearchOptions{Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	// Non-vacuity FIRST: an empty payload satisfies every absence assertion below
	// for free, so the test states what it needs before it asserts an absence.
	if len(response.Results) == 0 {
		t.Fatal("search returned no results at all; the absence assertions below would be vacuous")
	}
	found := make(map[string]bool, len(response.Results))
	for _, result := range response.Results {
		found[result.FilePath] = true
	}
	for _, control := range credentialStoreControlPaths {
		if !found[control] {
			t.Errorf("control file %s is missing from the payload; the corpus was not searched, "+
				"or the exclusion swallowed first-party source: got %v",
				control, searchResultPaths(response.Results))
		}
	}

	for _, result := range response.Results {
		for _, victim := range credentialStoreVictimPaths {
			if result.FilePath == victim {
				t.Errorf("search returned credential store %s at rank %d (score %.4f); snippet: %q",
					result.FilePath, result.Rank, result.Score, result.Snippet)
			}
		}
		assertNoCredentialMaterial(t, "result "+result.FilePath+" snippet", result.Snippet)
		for _, passage := range result.Passages {
			assertNoCredentialMaterial(t, "result "+result.FilePath+" passage", passage.Snippet)
		}
	}
}

// credentialStoreLiteralFiles is every FIRST-PARTY file in the fixture that names
// STRIPE_SECRET_KEY. The literal cluster reports repository-wide totals, so this
// count is what the totals must equal once the credential stores are excluded;
// `.env` and `deploy/secrets/prod-secrets.yaml` name it too, and before the fix
// they were counted and listed.
var credentialStoreLiteralFiles = []string{
	"internal/billing/service.go", "internal/config/dotenv.go", "internal/config/keys.go",
	"internal/http/client.go", "internal/invoices/service.go", "internal/payouts/service.go",
	"internal/refunds/service.go", "internal/secrets/manager.go", "pkg/credentials/provider.go",
}

// TestCredentialStoresAreAbsentFromLiteralCluster covers the second, independent
// route from a file's bytes to the agent's context.
//
// The literal cluster is built from the needle index, and the needle index answers
// a literal it cannot price from the posting lists with `git grep` — which Git runs
// over the WHOLE TREE rather than over the listed corpus. That is the one route
// that can still name a path the listing denied, which is why the fix has a second
// application point there and why this test exists separately from the ranked-payload
// one.
//
// TopK is pinned low on purpose: the block lists only sites the ranked payload did
// NOT already print, so a fixture where every naming file is printed produces an
// empty block and the test would assert nothing.
//
// The predecessor of this test asserted absences inside `if cluster != nil`, `for
// range response.FileOutlines` and `if closedSet != nil`, and on this fixture all
// three were empty — it passed against a binary that returned `.env`, `.npmrc`,
// `credentials.json` and `certs/server.pem` in the ranked payload with their
// contents in the snippets. Hence the explicit non-vacuity fatals, the positive
// control, and the exact totals assertion below.
func TestCredentialStoresAreAbsentFromLiteralCluster(t *testing.T) {
	t.Parallel()

	repo := credentialStoreRepo(t)
	response, err := SearchRepository(t.Context(), repo, "test-version",
		"stripe secret key credentials",
		SearchOptions{
			Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir(),
			EditSiteBodies: true, TopK: 3,
		})
	if err != nil {
		t.Fatal(err)
	}

	cluster := response.LiteralCluster
	if cluster == nil {
		t.Fatal("no literal cluster was built, so every absence assertion below would be vacuous. " +
			"This fixture is shaped to make the block fire — one distinctive literal, a live EDIT " +
			"site, and more naming files than TopK prints. If the block's own gates changed, " +
			"reshape the fixture; do not delete the assertions")
	}
	if cluster.Literal != "STRIPE_SECRET_KEY" {
		t.Fatalf("literal = %q, want STRIPE_SECRET_KEY; the fixture no longer drives the block "+
			"through the credential-bearing literal", cluster.Literal)
	}
	if len(cluster.Hits) == 0 {
		t.Fatal("literal cluster listed no hits; the absence assertions below would be vacuous")
	}

	// Positive control: the block must have mined real code, or "no credential store
	// in it" says nothing at all.
	listed := make(map[string]bool, len(cluster.Hits))
	for _, hit := range cluster.Hits {
		listed[hit.FilePath] = true
	}
	if !listed["internal/config/keys.go"] {
		t.Errorf("literal cluster did not list the EDIT site internal/config/keys.go; it must "+
			"exclude the credential stores without going blind: %+v", cluster.Hits)
	}

	for _, hit := range cluster.Hits {
		for _, victim := range credentialStoreVictimPaths {
			if hit.FilePath == victim {
				t.Errorf("literal cluster listed credential store %s (line %d, body %q)",
					hit.FilePath, hit.Line, hit.Body)
			}
		}
		assertNoCredentialMaterial(t, "literal cluster hit "+hit.FilePath, hit.Body)
	}

	// The repository-wide totals are the block's whole promise ("you have now seen
	// every place this concept is named"), and they are computed from the same
	// git-grep sweep. Counting a credential store here is the leak in its quietest
	// form: the file is not named, but the agent is told to go looking for it.
	if cluster.FilesTotal != len(credentialStoreLiteralFiles) {
		t.Errorf("literal cluster counts %d files naming %s, want %d (%v); a credential store "+
			"is still inside the sweep", cluster.FilesTotal, cluster.Literal,
			len(credentialStoreLiteralFiles), credentialStoreLiteralFiles)
	}

	for _, outline := range response.FileOutlines {
		for _, victim := range credentialStoreVictimPaths {
			if outline.FilePath == victim {
				t.Errorf("file outline described credential store %s", victim)
			}
		}
	}
	if closedSet := response.ClosedSet; closedSet != nil {
		for _, site := range closedSet.Sites {
			for _, victim := range credentialStoreVictimPaths {
				if site.FilePath == victim {
					t.Errorf("closed-set block cited credential store %s", victim)
				}
			}
		}
	}
}

// TestCredentialStoresAreAbsentFromHeadSearchResults runs the same assertion
// against the COMMITTED tree, which is a different listing built by a different
// loader.
//
// It is the weaker of the two paths and was the one with no protection at all:
// loadExplicitIgnoreMatcher reads .graphignore and the caller's explicit files
// and NOT .gitignore, so a committed credential store was searchable under
// `--head` even in a repository whose .gitignore names it.
func TestCredentialStoresAreAbsentFromHeadSearchResults(t *testing.T) {
	t.Parallel()

	repo := credentialStoreRepo(t)
	response, err := SearchRepository(t.Context(), repo, "test-version",
		"api key credentials token",
		SearchOptions{Profile: ProfileFull, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 {
		t.Fatal("--head search returned no results at all; the absence assertions below would be vacuous")
	}
	found := make(map[string]bool, len(response.Results))
	for _, result := range response.Results {
		found[result.FilePath] = true
	}
	for _, control := range credentialStoreControlPaths {
		if !found[control] {
			t.Errorf("control file %s is missing from the --head payload: got %v",
				control, searchResultPaths(response.Results))
		}
	}
	for _, result := range response.Results {
		for _, victim := range credentialStoreVictimPaths {
			if result.FilePath == victim {
				t.Errorf("--head search returned credential store %s at rank %d; snippet: %q",
					result.FilePath, result.Rank, result.Snippet)
			}
		}
		assertNoCredentialMaterial(t, "--head result "+result.FilePath+" snippet", result.Snippet)
	}
}

// TestCredentialStoresProduceNoGraphSymbols pins the other half of what moving
// the exclusion into the ignore matcher bought: the snapshot is parsed from the
// same listing, so a credential store is absent from `entire graph symbols` too,
// not only from search.
func TestCredentialStoresProduceNoGraphSymbols(t *testing.T) {
	t.Parallel()

	repo := credentialStoreRepo(t)
	snapshot, err := BuildProviderSnapshotWithOptions(t.Context(), repo, "test-version",
		ProviderSnapshotOptions{NoNetwork: true, Worktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Symbols) == 0 {
		t.Fatal("snapshot holds no symbols at all; the absence assertion below would be vacuous")
	}
	indexed := make(map[string]bool, len(snapshot.Symbols))
	for _, symbol := range snapshot.Symbols {
		indexed[symbol.FilePath] = true
	}
	for _, control := range credentialStoreControlPaths {
		if !indexed[control] {
			t.Errorf("control file %s produced no symbols; the exclusion swallowed first-party source", control)
		}
	}
	for _, symbol := range snapshot.Symbols {
		for _, victim := range credentialStoreVictimPaths {
			if symbol.FilePath == victim {
				t.Errorf("snapshot indexed credential store %s as symbol %q (%s)",
					symbol.FilePath, symbol.Name, symbol.Kind)
			}
		}
	}
}

// TestIncludeFileOverridesCredentialStoreExclusion pins the documented escape
// hatch end to end: the built-in deny is loaded before the caller's explicit
// --include-file, so a caller who deliberately asks for a path gets it.
//
// A default-deny with no override is a trap for the repository that keeps a
// checked-in `.env.example` as its configuration documentation.
func TestIncludeFileOverridesCredentialStoreExclusion(t *testing.T) {
	t.Parallel()

	repo := credentialStoreRepo(t)
	includeFile := writeCredentialIncludeFile(t, ".env\n")

	response, err := SearchRepository(t.Context(), repo, "test-version",
		"api key credentials token",
		SearchOptions{
			Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir(),
			IncludeFiles: []string{includeFile},
		})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range response.Results {
		if result.FilePath == ".env" {
			return
		}
	}
	t.Errorf("--include-file did not re-admit the path it names; the built-in deny must stay "+
		"overridable by an explicit caller request: got %v", searchResultPaths(response.Results))
}

// TestCredentialReadingCodeStillRanksForConfigQueries is the anti-overreach
// guard: excluding the credential FILES must not cost the caller the CODE that
// reads them, which is the legitimate answer to a dotenv question.
func TestCredentialReadingCodeStillRanksForConfigQueries(t *testing.T) {
	t.Parallel()

	repo := credentialStoreRepo(t)
	response, err := SearchRepository(t.Context(), repo, "test-version",
		"dotenv config loading",
		SearchOptions{Worktree: true, Profile: ProfileFull, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range response.Results {
		if result.FilePath == "internal/config/dotenv.go" {
			return
		}
	}
	t.Errorf("searching for the dotenv loading CODE no longer finds it; got %v",
		searchResultPaths(response.Results))
}
