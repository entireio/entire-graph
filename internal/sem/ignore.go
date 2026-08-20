package sem

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

type ignoreMatcher struct {
	rules           []ignoreRule
	parsedRuleCount int
}

type ignoreRule struct {
	ignore      bool
	includeFile bool
	directory   bool
	// fileOnly restricts the rule to non-directory paths: basename-only rules match
	// the final segment, while path-shaped rules match the full relative path. It
	// never matches a directory or an ancestor directory segment. Ordinary gitignore
	// syntax cannot express that, so it is set only for built-in entries (see
	// builtinSecretFileOnlyPatterns).
	fileOnly     bool
	basenameOnly bool
	pattern      string
	expression   *regexp.Regexp
}

type ignoreMatchKind int

const (
	ignoreNoMatch ignoreMatchKind = iota
	ignoreAncestorMatch
	ignoreSelfMatch
)

// graphIgnoreFileName is a repo-root ignore list the graph honors in addition to
// .gitignore, using the same gitignore syntax. It exists for paths that are
// tracked in git on purpose (so .gitignore cannot exclude them) yet should be
// kept out of the code graph — e.g. vendored or generated sources such as the
// multi-MB tree-sitter parser.c blobs, which only ever produce E_FILE_TOO_LARGE /
// E_PARSE_ERROR noise and a false "degraded" completeness. It is loaded with the
// same authority as the root .gitignore, before any explicit --ignore-file, so a
// caller's --include-file can still override it.
const graphIgnoreFileName = ".graphignore"

const (
	// Root and explicit ignore inputs affect both the live provider corpus and
	// replay admission. Keep their resource contract identical and bounded before
	// parsing can retain an attacker-controlled number or size of regular
	// expressions. The rule count is cumulative across every external file loaded
	// into one matcher; the fixed, trusted built-in secret rules are not charged to
	// that budget.
	maxIgnoreFileBytes   = 1 << 20
	maxIgnoreRuleBytes   = 64 << 10
	maxIgnoreParsedRules = 16 << 10

	// A linked worktree resolves info/exclude through these small Git pointer
	// files. Git writes one path line to each; bounding them prevents ignore-policy
	// discovery itself from becoming an unbounded read.
	maxGitIndirectionFileBytes = 4 << 10
)

// Built-in credential-store exclusion
// ===================================
//
// A credential store is a file whose CONTENT is the secret: `.env`, `.npmrc`, a
// PEM private key, a service-account `credentials.json`, a Kubernetes Secret
// manifest under `deploy/secrets/`. Nothing in the graph asked whether a file
// was one before reading it, so `entire graph search` read them, ranked them and
// quoted the matching region back as a snippet, putting a repository's secrets
// into the calling agent's LLM context (CWE-538 / CWE-312). They match readily:
// the key names AROUND the secret (`STRIPE_SECRET_KEY`, `_authToken`,
// `private_key`) are exactly the vocabulary of a query about authentication.
//
// The exclusion lives here, in the ignore matcher, because this is the one place
// that governs every corpus at once. The working-tree listing consults it at
// provider.go worktreeSourceFiles, the committed-tree listing at
// filterIgnoredPaths, and both listings are what the snapshot is parsed from —
// so a path denied here is absent from search results, from the context blocks,
// and from `entire graph symbols`, without a second taxonomy anywhere.
//
// It is an EXCLUSION rather than a ranking penalty on purpose. searchFileClassPrior
// (search_file_class.go) documents itself as "not a filter: a non-source hit stays
// reachable (and still ranks first when nothing else matches at all)", and every
// class prior is switched back off when the query names the class — so
// "api key credentials token", the query most likely to surface a secret, would
// restore a credential file to full strength. The harm here is the bytes being
// quoted at all, not the rank.
//
// Two properties of where it is loaded matter:
//
//   - It is loaded AFTER the repository's own exclude files (.gitignore,
//     .graphignore, info/exclude) so a negation shipped inside the repository
//     under analysis cannot switch it off, and BEFORE the caller's explicit
//     --ignore-file/--include-file so `--include-file` remains the documented,
//     deliberate override. Later rules win in ignoreMatcher.decide.
//   - The patterns are matched case-insensitively, unlike ordinary gitignore
//     rules, because `.ENV` on a case-insensitive filesystem is the same file
//     and the same secret.
//
// Scope is the credential STORE, never code that talks about credentials. Every
// rule is decided on a basename, suffix, or exact tool-owned path. The broader
// `secrets/`-directory rules additionally require a data or config suffix — so
// `internal/secrets/manager.go`, `pkg/credentials/provider.go` and
// `internal/config/dotenv.go` stay fully searchable.
// It is a var rather than a const for one reason: the persistent caches key on a
// digest of it (see builtinSecretRulesDigest), and a test proving that binding has
// to be able to stand in for a differently-built binary. Production code never
// assigns to it.
var builtinSecretIgnorePatterns = `
# Dotenv and direnv: the whole file is credential material. The .env.<environment>
# variants are covered because they are the same file shape, and the template forms
# (.env.example, .env.sample) with them: a template is byte-shaped exactly like the
# real thing and is routinely committed with real values still in it.
.env
.env.*
*.env
.envrc

# Registry, database and service credential files, by their conventional names.
.npmrc
.netrc
_netrc
.pgpass
.htpasswd
.pypirc
.dockercfg
.boto
.git-credentials

# SSH private keys. The .pub half is deliberately NOT matched: publishing it is
# its purpose, and id_rsa here matches only the exact basename.
id_rsa
id_dsa
id_ecdsa
id_ed25519

# Conventional credential and secret store filenames. The bare credentials entry
# is the AWS CLI shape (.aws/credentials). It is carried as FILE-ONLY
# (builtinSecretFileOnlyPatterns): a bare gitignore pattern matches every path
# segment rather than only the basename, so without that it would also swallow a
# SOURCE package directory named credentials/ and everything under it. File-only
# matching is used instead of a "!credentials/" negation because this block is
# loaded AFTER the repository own exclude files in order to outrank them, so any
# negation here would also cancel a repository own "credentials/" exclusion.
credentials
credentials.json
credentials.yml
credentials.yaml
credentials.ini
credentials.toml
secrets.json
secrets.yml
secrets.yaml
secrets.ini
secrets.toml

# Exact tool-owned stores whose canonical paths or filenames identify credential
# material. These are file-only even when the pattern is path-shaped: a directory
# literally named config.json must not hide the source tree beneath it.
**/.docker/config.json
**/.kube/config
credentials.tfrc.json
application_default_credentials.json

# Key material and encrypted stores, by suffix. .crt, .cer and .pub are deliberately
# absent: they are the public halves, and excluding them would cost recall and
# protect nothing.
*.pem
*.key
*.pfx
*.p12
*.pkcs12
*.jks
*.keystore
*.truststore
*.ppk
*.kdbx
*.asc
*.gpg

# Path-shaped stores: a data or config file under a directory segment named
# secrets/ or credentials/, at any depth. This is the Kubernetes / sops /
# sealed-secrets convention, where the basename carries no signal at all
# (deploy/secrets/prod-secrets.yaml). Restricted to data and config suffixes so a
# SOURCE package named secrets/ or credentials/ stays fully searchable.
**/secrets/**/*.yaml
**/secrets/**/*.yml
**/secrets/**/*.json
**/secrets/**/*.ini
**/secrets/**/*.toml
**/secrets/**/*.cfg
**/secrets/**/*.conf
**/secrets/**/*.properties
**/secrets/**/*.txt
**/secrets/**/*.enc
**/credentials/**/*.yaml
**/credentials/**/*.yml
**/credentials/**/*.json
**/credentials/**/*.ini
**/credentials/**/*.toml
**/credentials/**/*.cfg
**/credentials/**/*.conf
**/credentials/**/*.properties
**/credentials/**/*.txt
**/credentials/**/*.enc
`

// builtinSecretIgnoreRules is builtinSecretIgnorePatterns parsed once. The rules
// are immutable and their regexps are safe for concurrent use, so every matcher
// shares this one slice.
var builtinSecretIgnoreRules = parseBuiltinSecretIgnoreRules()

// builtinSecretFileOnlyPatterns are the built-in entries that must deny a FILE and
// leave any matching directory alone. Gitignore syntax has no way to say "file
// only" — a bare pattern matches every path segment and a path-shaped pattern can
// match an ancestor — and the one thing that looks like it, a trailing-slash
// negation such as `!credentials/`,
// cannot be used here: this block is loaded after the repository's own exclude
// files so that it outranks them, which means a negation in it also cancels a
// repository's own `credentials/` exclusion and re-admits every file underneath.
var builtinSecretFileOnlyPatterns = map[string]struct{}{
	"credentials":                          {},
	".git-credentials":                     {},
	"**/.docker/config.json":               {},
	"**/.kube/config":                      {},
	"credentials.tfrc.json":                {},
	"application_default_credentials.json": {},
}

func parseBuiltinSecretIgnoreRules() []ignoreRule {
	var matcher ignoreMatcher
	if err := matcher.loadContent(builtinSecretIgnorePatterns, false); err != nil {
		// loadContent only fails on a scanner error, which a string reader cannot
		// produce; a panic here would mean the block above stopped being a string.
		panic("sem: built-in credential-store ignore rules failed to parse: " + err.Error())
	}
	for index := range matcher.rules {
		rule := &matcher.rules[index]
		rule.expression = regexp.MustCompile("(?i)" + rule.expression.String())
		if _, ok := builtinSecretFileOnlyPatterns[rule.pattern]; ok {
			if rule.directory || !rule.ignore {
				panic("sem: file-only built-in rule " + rule.pattern + " is not a file deny")
			}
			rule.fileOnly = true
		}
	}
	return matcher.rules
}

// builtinSecretRulesDigest fingerprints the built-in credential-store taxonomy so
// the persistent cache keys can bind to it. Both caches store a corpus whose
// MEMBERSHIP this taxonomy decides, and nothing else in either key separates two
// builds that disagree about it: the provider version is the release string, which
// the repository's own `mise run build` leaves at "dev", so a build made before
// these rules existed and a build made after them key identically. An entry warmed
// by the earlier build is then served to the later one, and the paths it names are
// re-emitted and reopened.
//
// A digest rather than a hand-bumped cache version, because the digest moves
// whenever the pattern block is edited and there is nothing left to remember. It
// also invalidates every entry already on disk, which is what a version bump would
// have been for.
func builtinSecretRulesDigest() string {
	sum := sha256.Sum256([]byte(builtinSecretIgnorePatterns))
	return hex.EncodeToString(sum[:])
}

// loadBuiltinSecretRules appends the built-in credential-store deny. Callers place
// it after the repository's own exclude files and before the caller's explicit
// ones; see the comment on builtinSecretIgnorePatterns for why that position.
func (m *ignoreMatcher) loadBuiltinSecretRules() {
	m.rules = append(m.rules, builtinSecretIgnoreRules...)
}

func loadWorktreeIgnoreMatcher(repo string, ignoreFiles, includeFiles []string) (ignoreMatcher, error) {
	var matcher ignoreMatcher
	if err := matcher.loadOptional(filepath.Join(repo, ".gitignore"), false); err != nil {
		return ignoreMatcher{}, err
	}
	if err := matcher.loadOptional(filepath.Join(repo, graphIgnoreFileName), false); err != nil {
		return ignoreMatcher{}, err
	}
	// info/exclude is the repository's private exclude list: same syntax and same
	// authority as the root .gitignore, and Git applies both. Reading only
	// .gitignore silently pulled excluded trees into the working-tree scan.
	//
	// It is NOT always at <repo>/.git/info/exclude. In a linked worktree, <repo>/.git
	// is a regular file holding "gitdir: <path>", so that join names a path under a
	// non-directory: os.Stat returns ENOTDIR rather than ErrNotExist, and treating
	// that as fatal aborted the entire search with zero results in every worktree.
	if exclude := gitInfoExcludePath(repo); exclude != "" {
		if err := matcher.loadOptional(exclude, false); err != nil {
			return ignoreMatcher{}, err
		}
	}
	matcher.loadBuiltinSecretRules()
	if err := matcher.loadExplicit(repo, ignoreFiles, includeFiles); err != nil {
		return ignoreMatcher{}, err
	}
	return matcher, nil
}

func loadExplicitIgnoreMatcher(repo string, ignoreFiles, includeFiles []string) (ignoreMatcher, error) {
	var matcher ignoreMatcher
	if err := matcher.loadOptional(filepath.Join(repo, graphIgnoreFileName), false); err != nil {
		return ignoreMatcher{}, err
	}
	matcher.loadBuiltinSecretRules()
	if err := matcher.loadExplicit(repo, ignoreFiles, includeFiles); err != nil {
		return ignoreMatcher{}, err
	}
	return matcher, nil
}

func (m *ignoreMatcher) loadExplicit(repo string, ignoreFiles, includeFiles []string) error {
	for _, ignoreFile := range ignoreFiles {
		resolved := ignoreFile
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(repo, resolved)
		}
		if err := m.loadRequired(resolved, false); err != nil {
			return err
		}
	}
	for _, includeFile := range includeFiles {
		resolved := includeFile
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(repo, resolved)
		}
		if err := m.loadRequired(resolved, true); err != nil {
			return err
		}
	}
	return nil
}

// gitInfoExcludePath resolves the info/exclude that Git itself would apply to a
// working tree, or "" when there is no git directory to consult.
//
// <repo>/.git is a directory in an ordinary clone but a regular file in a linked
// worktree, where it holds "gitdir: <path to .git/worktrees/<name>>". Git shares
// info/ across worktrees via that gitdir's commondir pointer, so the exclude file
// lives under the common directory, not under <repo>/.git.
func gitInfoExcludePath(repo string) string {
	dotGit := filepath.Join(repo, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return filepath.Join(dotGit, "info", "exclude")
	}
	if !info.Mode().IsRegular() {
		return ""
	}
	raw, err := readSmallRegularFile(dotGit, maxGitIndirectionFileBytes)
	if err != nil {
		return ""
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(raw)), "gitdir:"))
	if gitDir == "" {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repo, gitDir)
	}
	// commondir points at the shared .git that owns info/; it may be relative to gitDir.
	if common, err := readSmallRegularFile(
		filepath.Join(gitDir, "commondir"),
		maxGitIndirectionFileBytes,
	); err == nil {
		if c := strings.TrimSpace(string(common)); c != "" {
			if !filepath.IsAbs(c) {
				c = filepath.Join(gitDir, c)
			}
			gitDir = filepath.Clean(c)
		}
	}
	return filepath.Join(gitDir, "info", "exclude")
}

func (m *ignoreMatcher) loadOptional(file string, includeMode bool) error {
	return m.loadPath(file, includeMode, false)
}

func (m *ignoreMatcher) loadRequired(file string, includeMode bool) error {
	return m.loadPath(file, includeMode, true)
}

func (m *ignoreMatcher) loadPath(file string, includeMode, required bool) error {
	label := ignoreFileLabel(includeMode)
	info, err := os.Stat(file)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		if !required {
			// ENOTDIR: a parent component is not a directory, so the file cannot
			// exist. For an optional exclude file that is absence, never a hard
			// failure.
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s %q does not exist", label, file)
		}
	}
	if err != nil {
		return fmt.Errorf("read %s %q: %w", label, file, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s %q is not a regular file", label, file)
	}
	if info.Size() > maxIgnoreFileBytes {
		return fmt.Errorf(
			"read %s %q: file exceeds %d bytes",
			label,
			file,
			maxIgnoreFileBytes,
		)
	}

	opened, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("read %s %q: %w", label, file, err)
	}
	defer opened.Close()
	openedInfo, err := opened.Stat()
	if err != nil {
		return fmt.Errorf("read %s %q: %w", label, file, err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return fmt.Errorf("%s %q changed while opening", label, file)
	}
	if openedInfo.Size() > maxIgnoreFileBytes {
		return fmt.Errorf(
			"read %s %q: file exceeds %d bytes",
			label,
			file,
			maxIgnoreFileBytes,
		)
	}
	if err := m.loadReader(io.LimitReader(opened, maxIgnoreFileBytes+1), includeMode); err != nil {
		return fmt.Errorf("read %s %q: %w", label, file, err)
	}
	return nil
}

func (m *ignoreMatcher) loadFile(file string, includeMode bool) error {
	return m.loadPath(file, includeMode, true)
}

func (m *ignoreMatcher) loadContent(content string, includeMode bool) error {
	return m.loadReader(strings.NewReader(content), includeMode)
}

func (m *ignoreMatcher) loadReader(source io.Reader, includeMode bool) error {
	reader := bufio.NewReaderSize(source, maxIgnoreRuleBytes+1)
	totalBytes := 0
	for {
		line, readErr := reader.ReadSlice('\n')
		totalBytes += len(line)
		if totalBytes > maxIgnoreFileBytes {
			return fmt.Errorf("ignore input exceeds %d bytes", maxIgnoreFileBytes)
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			return fmt.Errorf("ignore rule line exceeds %d bytes", maxIgnoreRuleBytes)
		}
		if len(line) > 0 {
			line = bytes.TrimSuffix(line, []byte{'\n'})
			if len(line) > maxIgnoreRuleBytes {
				return fmt.Errorf("ignore rule line exceeds %d bytes", maxIgnoreRuleBytes)
			}
			rule, ok := parseIgnoreRule(string(line), includeMode)
			if ok {
				if m.parsedRuleCount >= maxIgnoreParsedRules {
					return fmt.Errorf("ignore inputs exceed %d parsed rules", maxIgnoreParsedRules)
				}
				m.rules = append(m.rules, rule)
				m.parsedRuleCount++
			}
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		return readErr
	}
}

func readSmallRegularFile(file string, limit int64) ([]byte, error) {
	info, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("file is not a regular file of at most %d bytes", limit)
	}
	opened, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	openedInfo, err := opened.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || openedInfo.Size() > limit {
		return nil, errors.New("file changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(opened, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return content, nil
}

func ignoreFileLabel(includeMode bool) string {
	if includeMode {
		return "include file"
	}
	return "ignore file"
}

func parseIgnoreRule(line string, includeMode bool) (ignoreRule, bool) {
	line = strings.TrimRight(line, "\r")
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ignoreRule{}, false
	}
	if strings.HasPrefix(line, `\#`) {
		line = line[1:]
	}
	negated := false
	if strings.HasPrefix(line, "!") {
		negated = true
		line = strings.TrimSpace(line[1:])
		if line == "" {
			return ignoreRule{}, false
		}
	}
	line = filepath.ToSlash(line)
	line = strings.TrimPrefix(line, "./")
	anchored := strings.HasPrefix(line, "/")
	line = strings.TrimLeft(line, "/")
	directory := strings.HasSuffix(line, "/")
	line = strings.TrimRight(line, "/")
	line = cleanIgnorePath(line)
	if line == "" {
		return ignoreRule{}, false
	}

	basenameOnly := !anchored && !strings.Contains(line, "/")
	ignore := !negated
	if includeMode {
		ignore = negated
	}
	return ignoreRule{
		ignore:       ignore,
		includeFile:  includeMode,
		directory:    directory,
		basenameOnly: basenameOnly,
		pattern:      line,
		expression:   regexp.MustCompile(globPatternExpression(line)),
	}, true
}

func (m ignoreMatcher) Ignored(rel string, isDir bool) bool {
	matched, ignored := m.decide(rel, isDir)
	return matched && ignored
}

// decide reports whether any rule matched rel and, when one did, the verdict of
// the winning rule (a rule matching the path itself beats one matching only an
// ancestor directory; within each of those, the last rule loaded wins). The
// caller needs "matched" separately from "ignored" so a stack of per-directory
// ignore files can let the deepest file that has an opinion decide, exactly as
// Git does.
func (m ignoreMatcher) decide(rel string, isDir bool) (bool, bool) {
	rel = cleanIgnorePath(rel)
	if rel == "" {
		return false, false
	}
	selfMatched := false
	selfIgnored := false
	ancestorMatched := false
	ancestorIgnored := false
	for _, rule := range m.rules {
		switch rule.matchKind(rel, isDir) {
		case ignoreSelfMatch:
			selfMatched = true
			selfIgnored = rule.ignore
		case ignoreAncestorMatch:
			ancestorMatched = true
			ancestorIgnored = rule.ignore
		}
	}
	if selfMatched {
		return true, selfIgnored
	}
	if ancestorMatched {
		return true, ancestorIgnored
	}
	return false, false
}

// decideSelf reports the verdict of the last rule that names the path itself
// rather than one of its ancestor directories — the most specific kind of rule,
// whichever file it came from.
func (m ignoreMatcher) decideSelf(rel string, isDir bool) (bool, bool) {
	rel = cleanIgnorePath(rel)
	if rel == "" {
		return false, false
	}
	matched := false
	ignored := false
	for _, rule := range m.rules {
		if rule.matchKind(rel, isDir) == ignoreSelfMatch {
			matched = true
			ignored = rule.ignore
		}
	}
	return matched, ignored
}

// Reincluded reports whether an explicit include file re-includes rel, which is
// the only way a path Git's own exclude rules cover may enter a listing at all.
// It gates whether such a path is considered; the merged ignore rules then make
// the final call, so an include file that reopens a directory does not override a
// rule naming one file inside it.
func (m ignoreMatcher) Reincluded(rel string, isDir bool) bool {
	rel = cleanIgnorePath(rel)
	if rel == "" {
		return false
	}
	for _, rule := range m.rules {
		if !rule.includeFile || rule.ignore {
			continue
		}
		if rule.matchKind(rel, isDir) != ignoreNoMatch {
			return true
		}
	}
	return false
}

// maxNestedIgnoreFileBytes bounds one .gitignore read during a walk. Real ignore
// files are a few kilobytes; anything past this is not an ignore file and must
// not be materialized just because it is named like one.
const maxNestedIgnoreFileBytes = maxIgnoreFileBytes

// nestedIgnoreStack applies per-directory .gitignore files during a walk the way
// Git does: a .gitignore governs its own subtree, and the deepest file with an
// opinion about a path wins. It is the filesystem-walk fallback's answer to the
// gap that put vendored dependency trees in the graph — a tree ignored by
// `backend/.gitignore` is invisible to a reader that only ever parsed the
// repository root's .gitignore.
type nestedIgnoreStack struct {
	repo   string
	base   ignoreMatcher
	levels []nestedIgnoreLevel
}

type nestedIgnoreLevel struct {
	dir     string
	matcher ignoreMatcher
}

func newNestedIgnoreStack(repo string, base ignoreMatcher) *nestedIgnoreStack {
	return &nestedIgnoreStack{repo: repo, base: base}
}

// enter registers the directory the walk is about to descend into (repo-relative,
// slash-separated; "" for the repository root) and loads its .gitignore, if any.
// Levels the walk has left are dropped, so the stack holds one matcher per
// ancestor directory of the current position.
func (s *nestedIgnoreStack) enter(dir string) error {
	dir = cleanIgnorePath(dir)
	kept := s.levels[:0]
	for _, level := range s.levels {
		if level.dir == dir || strings.HasPrefix(dir, level.dir+"/") {
			kept = append(kept, level)
		}
	}
	s.levels = kept
	if dir == "" {
		// The root .gitignore is already part of base, alongside the explicit
		// ignore/include files that must keep overriding it.
		return nil
	}
	file := filepath.Join(s.repo, filepath.FromSlash(dir), ".gitignore")
	info, err := os.Stat(file)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read nested ignore file %q: %w", file, err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if info.Size() > maxNestedIgnoreFileBytes {
		return fmt.Errorf("read nested ignore file %q: file exceeds %d bytes", file, maxNestedIgnoreFileBytes)
	}
	var matcher ignoreMatcher
	if err := matcher.loadFile(file, false); err != nil {
		return fmt.Errorf("read nested ignore file %q: %w", file, err)
	}
	s.levels = append(s.levels, nestedIgnoreLevel{dir: dir, matcher: matcher})
	return nil
}

// Ignored reports the stack's verdict for a repo-relative path.
//
// Precedence, most specific first: a rule that names the path itself (from the
// root .gitignore, .git/info/exclude, or an explicit ignore/include file), then
// the deepest nested .gitignore with an opinion, then the remaining
// directory-level rules of the root set. That ordering is what lets a project
// ignore `cache/` and still name `cache/skip.py`, while a nested
// `backend/.gitignore` keeps its own subtree's verdict.
func (s *nestedIgnoreStack) Ignored(rel string, isDir bool) bool {
	if matched, ignored := s.base.decideSelf(rel, isDir); matched {
		return ignored
	}
	rel = cleanIgnorePath(rel)
	for i := len(s.levels) - 1; i >= 0; i-- {
		level := s.levels[i]
		sub, ok := pathUnder(level.dir, rel)
		if !ok {
			continue
		}
		if matched, ignored := level.matcher.decide(sub, isDir); matched {
			return ignored
		}
	}
	return s.base.Ignored(rel, isDir)
}

// MayIncludeDescendant defers to the explicit include files: only they can pull a
// path back out of an ignored directory, so only they can keep one walked.
func (s *nestedIgnoreStack) MayIncludeDescendant(rel string) bool {
	return s.base.MayIncludeDescendant(rel)
}

// ReincludesDescendant answers the vendored-directory heuristic over the whole
// stack: a negation in any ignore file on the current path — root or nested —
// declares part of that tree first-party.
func (s *nestedIgnoreStack) ReincludesDescendant(rel string) bool {
	if s.base.ReincludesDescendant(rel) {
		return true
	}
	for _, level := range s.levels {
		if level.matcher.reincludesDescendantUnder(level.dir, rel) {
			return true
		}
	}
	return false
}

// maxNestedIgnoreFiles bounds how many per-directory .gitignore files one listing
// merges. A repository with more ignore files than this is not a repository whose
// vendored-tree verdict hinges on the last one.
const maxNestedIgnoreFiles = 512

// nestedIgnoreRules merges the repository's per-directory .gitignore files for a
// listing that is not a walk — the committed-tree listing and Git's own
// working-tree listing both arrive as a flat path set, so there is no walk
// position to hang a stack off.
//
// It exists for one question: whether the project's own exclude rules re-include
// part of a tree the vendored-directory heuristic would otherwise skip. Reading
// only the root .gitignore answered "no" for every project that keeps those rules
// where Git expects them — beside the tree — which silently dropped tracked
// first-party source (`vendor/.gitignore` holding `*` and `!mypkg/` lost
// `vendor/mypkg/**` from both `--head` and the working tree, while the identical
// negation at the root kept it).
type nestedIgnoreRules struct {
	base   ignoreMatcher
	levels []nestedIgnoreLevel
}

func newNestedIgnoreRules(base ignoreMatcher) *nestedIgnoreRules {
	return &nestedIgnoreRules{base: base}
}

// addFile registers the parsed content of the .gitignore at repo-relative path
// file. Content that does not parse, or one file past the cap, is skipped: this
// is a heuristic's escape hatch, not a correctness boundary.
func (r *nestedIgnoreRules) addFile(file, content string) {
	dir := cleanIgnorePath(path.Dir(filepath.ToSlash(file)))
	if dir == "" || len(r.levels) >= maxNestedIgnoreFiles {
		return
	}
	var matcher ignoreMatcher
	if err := matcher.loadContent(content, false); err != nil {
		return
	}
	r.levels = append(r.levels, nestedIgnoreLevel{dir: dir, matcher: matcher})
}

// ReincludesDescendant reports whether the root rules or any nested .gitignore
// negate a path at or below rel.
func (r *nestedIgnoreRules) ReincludesDescendant(rel string) bool {
	if r.base.ReincludesDescendant(rel) {
		return true
	}
	for _, level := range r.levels {
		if level.matcher.reincludesDescendantUnder(level.dir, rel) {
			return true
		}
	}
	return false
}

// pathUnder returns rel expressed relative to dir when dir contains it.
func pathUnder(dir, rel string) (string, bool) {
	if dir == "" {
		return rel, true
	}
	if !strings.HasPrefix(rel, dir+"/") {
		return "", false
	}
	return strings.TrimPrefix(rel, dir+"/"), true
}

func (m ignoreMatcher) MayIncludeDescendant(rel string) bool {
	rel = cleanIgnorePath(rel)
	if rel == "" {
		return false
	}
	for _, rule := range m.rules {
		if rule.includeFile && !rule.ignore && rule.mayMatchDescendant(rel) {
			return true
		}
	}
	return false
}

// ReincludesDescendant reports whether the ignore rules negate (re-include) a
// specific path under rel — the project declares part of that tree as
// first-party. An erlang.mk/rebar monorepo gitignores fetched dependencies
// (`/deps/*`) but negates its own applications (`!/deps/rabbit/`), so the
// vendored-directory-name heuristic must not skip the tree wholesale; the
// ignore rules themselves keep the fetched dependencies out. Basename-only
// negations (e.g. `!.keep`) carry no path and are not treated as a signal.
func (m ignoreMatcher) ReincludesDescendant(rel string) bool {
	return m.reincludesDescendantUnder("", rel)
}

// reincludesDescendantUnder is ReincludesDescendant for an ignore file that lives
// in dir rather than at the repository root: its patterns are relative to dir, so
// each literal prefix is resolved against dir before being compared to rel.
// Basename-only negations carry no path in either position and are skipped in
// both, exactly as before.
func (m ignoreMatcher) reincludesDescendantUnder(dir, rel string) bool {
	rel = cleanIgnorePath(rel)
	if rel == "" {
		return false
	}
	dir = cleanIgnorePath(dir)
	for _, rule := range m.rules {
		if rule.ignore || rule.includeFile || rule.basenameOnly {
			continue
		}
		prefix := literalPatternPrefix(rule.pattern)
		if prefix == "" {
			continue
		}
		if dir != "" {
			prefix = dir + "/" + prefix
		}
		if prefix == rel || strings.HasPrefix(prefix, rel+"/") {
			return true
		}
	}
	return false
}

func (r ignoreRule) matchKind(rel string, isDir bool) ignoreMatchKind {
	if r.fileOnly {
		return r.matchFileOnly(rel, isDir)
	}
	if r.basenameOnly {
		return r.matchBasename(rel, isDir)
	}
	return r.matchPath(rel, isDir)
}

// matchFileOnly decides a rule that names a file and nothing else. Basename-only
// rules match the last segment; path-shaped rules match the complete relative
// path. Neither can produce an ancestor match — which is what keeps a credential
// filename from covering a same-named source directory and everything beneath it.
func (r ignoreRule) matchFileOnly(rel string, isDir bool) ignoreMatchKind {
	if isDir {
		return ignoreNoMatch
	}
	candidate := rel
	if r.basenameOnly {
		if slash := strings.LastIndex(rel, "/"); slash >= 0 {
			candidate = rel[slash+1:]
		}
	}
	if candidate != "" && r.expression.MatchString(candidate) {
		return ignoreSelfMatch
	}
	return ignoreNoMatch
}

func (r ignoreRule) matchBasename(rel string, isDir bool) ignoreMatchKind {
	segments := strings.Split(rel, "/")
	last := len(segments) - 1
	if r.directory {
		for i, segment := range segments {
			if i == last && !isDir {
				continue
			}
			if r.expression.MatchString(segment) {
				if i == last {
					return ignoreSelfMatch
				}
				return ignoreAncestorMatch
			}
		}
		return ignoreNoMatch
	}
	for i, segment := range segments {
		if r.expression.MatchString(segment) {
			if i == last {
				return ignoreSelfMatch
			}
			return ignoreAncestorMatch
		}
	}
	return ignoreNoMatch
}

func (r ignoreRule) matchPath(rel string, isDir bool) ignoreMatchKind {
	if !r.directory && r.expression.MatchString(rel) {
		return ignoreSelfMatch
	}
	if r.directory && isDir && r.expression.MatchString(rel) {
		return ignoreSelfMatch
	}
	for _, ancestor := range ancestorPaths(rel) {
		if r.expression.MatchString(ancestor) {
			return ignoreAncestorMatch
		}
	}
	return ignoreNoMatch
}

func (r ignoreRule) mayMatchDescendant(rel string) bool {
	if r.basenameOnly {
		return true
	}
	prefix := literalPatternPrefix(r.pattern)
	if prefix == "" {
		return true
	}
	return prefix == rel || strings.HasPrefix(prefix, rel+"/") || strings.HasPrefix(rel, prefix+"/")
}

func ancestorPaths(rel string) []string {
	parts := strings.Split(rel, "/")
	if len(parts) <= 1 {
		return nil
	}
	out := make([]string, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[:i], "/"))
	}
	return out
}

func cleanIgnorePath(value string) string {
	value = filepath.ToSlash(value)
	value = strings.TrimPrefix(value, "./")
	cleaned := path.Clean(value)
	if cleaned == "." {
		return ""
	}
	return strings.TrimPrefix(cleaned, "/")
}

func literalPatternPrefix(pattern string) string {
	index := strings.IndexAny(pattern, "*?[")
	if index >= 0 {
		pattern = pattern[:index]
	}
	return strings.Trim(strings.TrimRight(cleanIgnorePath(pattern), "/"), "/")
}

func globPatternExpression(pattern string) string {
	var out strings.Builder
	out.WriteString("^")
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					out.WriteString("(?:.*/)?")
					i += 3
					continue
				}
				out.WriteString(".*")
				i += 2
				continue
			}
			out.WriteString(`[^/]*`)
			i++
		case '?':
			out.WriteString(`[^/]`)
			i++
		default:
			out.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	out.WriteString("$")
	return out.String()
}
