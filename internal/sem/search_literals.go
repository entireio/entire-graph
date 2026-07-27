package sem

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Same-concept literals: `grep` folded into `search`
// ==================================================
//
// The call a real agent rated the best of its session was a repo-wide grep for ONE distinctive
// string — an enum constant's value — annotated with the enclosing symbol of every hit. 460 bytes
// told it three things it would otherwise have spent turns on: where the concept is DEFINED, which
// call sites only PASS it (and therefore need no change), and that the concept is also written down
// in the docs. It ruled out an entire hypothesis (a second registry in another layer) in one block.
//
// That is exactly the property the reference blocks that were measured as costly do not have: this
// block REPLACES A TOOL CALL the agent was going to make anyway. It is not extra context.
//
// What makes it safe to include by default is that it refuses far more often than it fires:
//
//   - ONE literal, chosen mechanically. No model, no ranking heuristic beyond "shares a word with
//     the query and is rare in this repository". Two literals would double the bytes to say the
//     same thing about the same concept.
//   - The literal must be DISTINCTIVE, and distinctiveness is measured, not assumed: the block is
//     emitted only when the repository-wide occurrence count is small. `toString` is not a concept
//     name, and the count is what proves it without a name list.
//   - The total is exact or the block does not exist. Its whole value is "you have now seen every
//     place this concept is named"; a sampled count silently destroys that.
//   - A hit is annotated only when the graph can say what encloses it. Unclassified hits are
//     counted, never guessed at — an EDIT label on a site that only reads the string sends an agent
//     to patch the wrong file.
const (
	// searchLiteralClusterMaxBytes caps the block on the larger of its two wire forms. Sized off
	// the measured win (~460 B of rendered text, four hits with their enclosing symbols) plus the
	// JSON schema around it.
	searchLiteralClusterMaxBytes = 560

	// searchLiteralHitLimit is how many occurrences the block lists. Past this the answer is "this
	// string is everywhere", which the distinctiveness cap has already rejected.
	searchLiteralHitLimit = 6

	// searchLiteralHitsPerFile stops one file with a dozen mentions from consuming the whole list.
	// Two is enough to show a file both declares and re-reads the literal.
	searchLiteralHitsPerFile = 2

	// searchLiteralMaxFiles is the distinctiveness cap, and it is measured in FILES rather than in
	// occurrences: a command name mentioned nine times inside one implementation file is still the
	// name of one concept, while the same count spread over ninety files is a magnet. It is a
	// property of the corpus, so it needs no vocabulary list to maintain.
	//
	// It doubles as the read budget for one lookup: a literal whose candidate set is larger than this
	// is not looked up at all, so a refusal costs zero IO.
	searchLiteralMaxFiles = 24

	// searchLiteralMaxOccurrences bounds the sweep in the other direction. Past it the literal is a
	// token of the language or of the framework, not of the change.
	searchLiteralMaxOccurrences = 60

	// searchLiteralMinOccurrences is the floor. A literal that occurs once occurs only in the hit
	// the payload already returned, so the block would say nothing.
	searchLiteralMinOccurrences = 2

	// searchLiteralMinLength and searchLiteralMaxLength bound what can be a concept name. Below the
	// floor a literal matches everything; above the ceiling it is a sentence, and grepping a
	// sentence answers a question nobody asked.
	searchLiteralMinLength = 4
	searchLiteralMaxLength = 48

	// searchLiteralCandidateLimit bounds how many candidate literals are looked up. Each lookup
	// costs file reads, and the needle index enforces its own global read budget on top.
	searchLiteralCandidateLimit = 3

	// searchLiteralRankedCandidates bounds how many candidates are PRICED before one is looked up.
	// Pricing off the posting lists is free — it reads a map, not a file — so it is done for many
	// more candidates than are then scanned.
	searchLiteralRankedCandidates = 12

	// searchLiteralGrepCandidates bounds how many candidates are priced through Git when the posting
	// lists cover none of them. Git is exact but costs a subprocess each, so only the best-ranked
	// candidates are worth one.
	searchLiteralGrepCandidates = 2

	// searchLiteralCandidatePathCap bounds the candidate SET one literal may be looked up over. It is
	// larger than searchLiteralMaxFiles because a candidate set is a superset: a posting list of forty
	// files can still contain a literal that occurs in three. It is smaller than the needle index's
	// whole budget so that the closed-set block, which is built first, always has reads left.
	searchLiteralCandidatePathCap = 48

	// searchLiteralAnchorMaxLines bounds how much of the top hit's region is mined for literals. A
	// whole-file "symbol" must not turn this into a corpus scan.
	searchLiteralAnchorMaxLines = 200
)

// Literal roles. Three words, because three is what the agent's own account needed: where the
// concept is defined, where it is only used, and where it is written about.
const (
	// SearchLiteralRoleEdit marks a site in a DECLARATION position: the literal sits at type level
	// (an enum constant, a constant table, a registry entry) rather than inside a callable body. It
	// is where a change to the concept normally lands.
	SearchLiteralRoleEdit = "EDIT"
	// SearchLiteralRoleConsumer marks a site inside a callable body: the code passes or reads the
	// literal. Knowing a site is a consumer is what lets an agent NOT open it.
	SearchLiteralRoleConsumer = "CONSUMER"
	// SearchLiteralRoleDoc marks prose or serialized data — it cannot execute, so it never breaks,
	// but it is usually what also has to be updated.
	SearchLiteralRoleDoc = "DOC"
)

// SearchLiteralCluster is every occurrence of one distinctive literal that names the queried
// concept.
type SearchLiteralCluster struct {
	// Literal is the exact string that was searched for, case-sensitively.
	Literal string `json:"literal"`
	// Hits are the listed occurrences, in path order.
	Hits []SearchLiteralHit `json:"hits"`
	// HitsTotal is how many occurrences exist in the repository, and FilesTotal in how many files —
	// not how many are listed. When they exceed the list the block was truncated and says so.
	HitsTotal  int `json:"hits_total"`
	FilesTotal int `json:"files_total"`
	// Unclassified counts FILES the graph holds no symbols for. They are reported as a number and
	// nothing more: without a symbol there is no honest role to give them.
	Unclassified int `json:"unclassified,omitempty"`
}

// SearchLiteralHit is one occurrence: where it is, what encloses it, and what that site does with
// the literal.
type SearchLiteralHit struct {
	FilePath string `json:"file_path"`
	Line     int    `json:"line"`
	// Symbol is the enclosing symbol's name, empty for a documentation hit.
	Symbol string `json:"symbol,omitempty"`
	Role   string `json:"role"`
}

// buildSearchLiteralCluster picks the literal and locates it, or returns nil.
//
// It returns nil far more often than not, and every one of those refusals is deliberate: no
// distinctive literal in the top hit, no query word inside it, no exact repository-wide count
// available, or a count that proves the literal is a magnet.
func buildSearchLiteralCluster(
	results []SearchResult,
	q searchQuery,
	symbolsByFile map[string][]SymbolRecord,
	index *searchNeedleIndex,
	maxBytes int,
) *SearchLiteralCluster {
	if index == nil || maxBytes <= 0 {
		return nil
	}
	anchor, ok := searchLiteralAnchor(results)
	if !ok {
		return nil
	}
	lines, ok := index.read(anchor.FilePath)
	if !ok {
		return nil
	}
	body := searchLiteralAnchorLines(strings.Split(lines, "\n"), anchor)
	priced := priceSearchLiteralCandidates(rankSearchLiteralCandidates(searchLiteralCandidates(body), q), index)
	for attempt, candidate := range priced {
		if attempt >= searchLiteralCandidateLimit {
			break
		}
		scan := index.locate(candidate.literal, candidate.paths, searchLiteralHitsPerFile, searchLiteralHitLimit*2)
		if !scan.complete || scan.files > searchLiteralMaxFiles {
			continue
		}
		if scan.occurrences < searchLiteralMinOccurrences || scan.occurrences > searchLiteralMaxOccurrences {
			continue
		}
		cluster := classifySearchLiteralHits(candidate.literal, scan, results, symbolsByFile)
		if cluster == nil {
			continue
		}
		if fitted := fitSearchLiteralCluster(cluster, maxBytes); fitted != nil {
			return fitted
		}
	}
	return nil
}

// searchLiteralCandidate is a candidate literal together with the file set that can contain it.
type searchLiteralCandidate struct {
	literal string
	paths   []string
}

// priceSearchLiteralCandidates resolves each candidate's candidate-file set and reorders them by how
// RARE the literal is, because rarity is what makes the block worth its bytes.
//
// Pricing is free: it reads posting lists, never files. The reorder matters — in a query about bind
// addresses the body offers both `bind` (21 files) and `default_bind` (3), and the ranking that put
// query-word equality first would have spent the block on the magnet. Rarity is the tie-break the
// distinctiveness cap is made of, so it is also the right sort key.
func priceSearchLiteralCandidates(literals []string, index *searchNeedleIndex) []searchLiteralCandidate {
	priced := make([]searchLiteralCandidate, 0, len(literals))
	for order, literal := range literals {
		if order >= searchLiteralRankedCandidates {
			break
		}
		paths, found := index.candidatePathsCheap(literal)
		if !found || len(paths) > searchLiteralCandidatePathCap {
			continue
		}
		priced = append(priced, searchLiteralCandidate{literal: literal, paths: paths})
	}
	// Nothing the posting lists can price. That happens when the literal shares no RARE word with
	// the query — a long compound identifier whose parts are all common — and it is exactly the case
	// Git answers exactly for one subprocess.
	if len(priced) == 0 && index.grep != nil {
		for order, literal := range literals {
			if order >= searchLiteralGrepCandidates {
				break
			}
			paths, found := index.grep(literal)
			if !found || len(paths) == 0 || len(paths) > searchLiteralMaxFiles {
				continue
			}
			sort.Strings(paths)
			priced = append(priced, searchLiteralCandidate{literal: literal, paths: paths})
		}
	}
	sort.SliceStable(priced, func(left, right int) bool {
		return len(priced[left].paths) < len(priced[right].paths)
	})
	return priced
}

// searchLiteralAnchor is the hit the literal is mined from: the highest-ranked candidate fix site.
// A documentation hit is not one, and neither is a related site — the concept's name has to come
// from the code the agent is about to edit.
func searchLiteralAnchor(results []SearchResult) (SearchResult, bool) {
	for _, result := range results {
		if result.Section != searchSectionPrimary {
			continue
		}
		if result.FilePath == "" || NonProgramTextPath(result.FilePath) {
			continue
		}
		return result, true
	}
	return SearchResult{}, false
}

// searchLiteralAnchorLines is the slice of the file the literals are mined from, bounded so a
// whole-file symbol cannot turn literal extraction into a corpus scan.
func searchLiteralAnchorLines(fileLines []string, anchor SearchResult) []string {
	start := anchor.StartLine
	if start < 1 {
		start = 1
	}
	end := anchor.EndLine
	if end < start {
		end = start
	}
	if end-start+1 > searchLiteralAnchorMaxLines {
		end = start + searchLiteralAnchorMaxLines - 1
	}
	if start > len(fileLines) {
		return nil
	}
	if end > len(fileLines) {
		end = len(fileLines)
	}
	return fileLines[start-1 : end]
}

// searchLiteralCandidates extracts the strings in a body that could name a concept: quoted string
// literals, constant-shaped identifiers, and compound identifiers. All three are language-agnostic
// surface facts, which is why no per-language table is involved.
//
// Identifiers are in scope and not only strings, because a concept is as often named by a symbol as
// by a literal, and a lexical sweep over an identifier finds what the graph's own edges cannot: the
// mentions in configuration, in fixtures, in prose, and behind dynamic dispatch. Only COMPOUND
// identifiers qualify (a case hump or an underscore): a single lowercase word is a word, and the
// distinctiveness cap would reject it anyway after paying to find out.
func searchLiteralCandidates(lines []string) []string {
	seen := map[string]bool{}
	candidates := make([]string, 0, 24)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if !searchLiteralShaped(value) || seen[value] {
			return
		}
		seen[value] = true
		candidates = append(candidates, value)
	}
	for _, line := range lines {
		for _, quoted := range searchQuotedLiterals(line) {
			add(quoted)
		}
		for _, constant := range searchConstantTokens(line) {
			add(constant)
		}
		for _, identifier := range searchCompoundIdentifiers(line) {
			add(identifier)
		}
	}
	return candidates
}

// searchCompoundIdentifiers returns the compound identifiers on a line: tokens carrying a case hump
// or an internal underscore. That shape is how every language in scope spells a name made of more
// than one word, and it is the shape that can be distinctive.
func searchCompoundIdentifiers(line string) []string {
	var out []string
	start := -1
	for offset := 0; offset <= len(line); offset++ {
		if offset < len(line) && searchIdentifierByte(line[offset]) {
			if start < 0 {
				start = offset
			}
			continue
		}
		if start >= 0 {
			if token := line[start:offset]; searchCompoundIdentifier(token) {
				out = append(out, token)
			}
			start = -1
		}
	}
	return out
}

// searchCompoundIdentifier reports whether a token is spelled from more than one word.
func searchCompoundIdentifier(token string) bool {
	if len(token) < searchLiteralMinLength {
		return false
	}
	lower, upperAfterLower, underscore := false, false, false
	for offset, character := range token {
		switch {
		case character == '_':
			// A leading or trailing underscore is a visibility convention, not a word boundary.
			underscore = underscore || (offset > 0 && offset < len(token)-1)
		case character >= 'a' && character <= 'z':
			lower = true
		case character >= 'A' && character <= 'Z':
			upperAfterLower = upperAfterLower || lower
		}
	}
	return underscore || upperAfterLower
}

// searchQuotedLiterals returns the contents of every single-line quoted string on a line. Quotes
// are matched pairwise and a backslash escape is skipped, which is the behavior every language in
// scope shares; a string that spans lines is simply not a candidate.
func searchQuotedLiterals(line string) []string {
	var out []string
	for offset := 0; offset < len(line); offset++ {
		quote := line[offset]
		if quote != '"' && quote != '\'' && quote != '`' {
			continue
		}
		end := -1
		for scan := offset + 1; scan < len(line); scan++ {
			if line[scan] == '\\' {
				scan++
				continue
			}
			if line[scan] == quote {
				end = scan
				break
			}
		}
		if end < 0 {
			break
		}
		out = append(out, line[offset+1:end])
		offset = end
	}
	return out
}

// searchConstantTokens returns the constant-shaped identifiers on a line: runs of upper-case
// letters, digits and underscores. That shape is how every language in scope spells an enum
// constant or a compile-time constant, so it needs no per-language rule.
func searchConstantTokens(line string) []string {
	var out []string
	start := -1
	uppers := 0
	flush := func(end int) {
		if start >= 0 && uppers >= 2 {
			out = append(out, line[start:end])
		}
		start, uppers = -1, 0
	}
	for offset := 0; offset < len(line); offset++ {
		character := rune(line[offset])
		switch {
		case character >= 'A' && character <= 'Z':
			if start < 0 {
				start = offset
			}
			uppers++
		case character == '_' || (character >= '0' && character <= '9'):
			if start < 0 {
				start = offset
			}
		default:
			flush(offset)
		}
	}
	flush(len(line))
	return out
}

// searchLiteralShaped is the shape gate: long enough to be a name, short enough not to be a
// sentence, carrying real letters rather than format punctuation (`%s`, `{}`, `---`), and not a
// PATH.
//
// Paths are excluded because a quoted path names a FILE, and the payload's own ranking is the tool
// for finding files. A measured miss: a Rust doc-include attribute put `../docs/routing/merge.md`
// forward as the concept name for a routing query.
func searchLiteralShaped(value string) bool {
	if len(value) < searchLiteralMinLength || len(value) > searchLiteralMaxLength {
		return false
	}
	if strings.ContainsAny(value, "/\\") || strings.HasPrefix(value, ".") {
		return false
	}
	letters := 0
	for _, character := range value {
		if character == '\n' || character == '\t' {
			return false
		}
		if unicode.IsLetter(character) {
			letters++
		}
	}
	return letters >= 3
}

// rankSearchLiteralCandidates keeps only the literals that name what the CALLER asked about, and
// orders them by how directly they do so.
//
// The query-overlap gate is what makes the block about the query rather than about whatever string
// happened to be in the body. Containment is checked in one direction only — the query word must be
// inside the literal — because that is the direction in which the query term's posting list is a
// superset of the literal's occurrences, and the exact repository-wide total depends on it.
func rankSearchLiteralCandidates(candidates []string, q searchQuery) []string {
	words := q.words
	if words == nil {
		words = searchQueryWords(q.rawLower)
	}
	type scored struct {
		literal string
		score   int
	}
	ranked := make([]scored, 0, len(candidates))
	for _, literal := range candidates {
		lower := strings.ToLower(literal)
		exact, contained := 0, 0
		for word := range words {
			if len(word) < searchLiteralMinLength || searchStopWords[word] {
				continue
			}
			if !strings.Contains(lower, word) {
				continue
			}
			contained++
			if lower == word {
				exact++
			}
		}
		if contained == 0 {
			continue
		}
		// A multi-word literal (an error message, a description) has to earn its place: one shared
		// word is a coincidence in prose, two is the message the caller quoted.
		if strings.ContainsAny(literal, " \t") && contained < 2 {
			continue
		}
		score := contained
		if exact > 0 {
			score += 4
		}
		if searchConstantShaped(literal) {
			score += 2
		}
		ranked = append(ranked, scored{literal: literal, score: score})
	}
	sort.SliceStable(ranked, func(left, right int) bool {
		if ranked[left].score != ranked[right].score {
			return ranked[left].score > ranked[right].score
		}
		if len(ranked[left].literal) != len(ranked[right].literal) {
			return len(ranked[left].literal) > len(ranked[right].literal)
		}
		return ranked[left].literal < ranked[right].literal
	})
	out := make([]string, 0, len(ranked))
	for _, entry := range ranked {
		out = append(out, entry.literal)
	}
	return out
}

// searchConstantShaped reports whether a value is spelled the way constants are: upper case,
// digits and underscores only.
func searchConstantShaped(value string) bool {
	letters := 0
	for _, character := range value {
		switch {
		case character >= 'A' && character <= 'Z':
			letters++
		case character == '_' || (character >= '0' && character <= '9'):
		default:
			return false
		}
	}
	return letters >= 2
}

// classifySearchLiteralHits turns raw occurrences into annotated hits, dropping the ones no honest
// role can be given and counting them separately.
func classifySearchLiteralHits(
	literal string,
	scan searchNeedleScan,
	results []SearchResult,
	symbolsByFile map[string][]SymbolRecord,
) *SearchLiteralCluster {
	cluster := &SearchLiteralCluster{
		Literal:    literal,
		HitsTotal:  scan.occurrences,
		FilesTotal: scan.files,
	}
	unclassifiedFiles := map[string]bool{}
	for _, hit := range scan.hits {
		// An occurrence inside source the payload has already PRINTED costs bytes to tell the reader
		// something on their screen. The repository totals still count it; the list does not repeat it.
		if searchLiteralHitAlreadyPrinted(results, hit) {
			continue
		}
		if NonProgramTextPath(hit.filePath) {
			cluster.Hits = append(cluster.Hits, SearchLiteralHit{
				FilePath: hit.filePath, Line: hit.line, Role: SearchLiteralRoleDoc,
			})
			continue
		}
		name, role, classified := searchLiteralRole(symbolsByFile[hit.filePath], hit)
		if !classified {
			// The graph holds no symbol covering this line and the line is indented, so it is inside
			// something the parser did not resolve. Neither "defines" nor "only uses" can be claimed
			// about it, and a role is the whole point of listing a hit. Count the FILE and move on —
			// counting occurrences would report the sample's size rather than the repository's.
			unclassifiedFiles[hit.filePath] = true
			continue
		}
		cluster.Hits = append(cluster.Hits, SearchLiteralHit{
			FilePath: hit.filePath, Line: hit.line, Symbol: name, Role: role,
		})
	}
	cluster.Unclassified = len(unclassifiedFiles)
	// One listed site is enough once the repository-wide totals are in the header: the totals are the
	// answer to "have I seen everywhere this concept is named", and a single site the payload did not
	// already print is still a grep the agent does not have to run.
	if len(cluster.Hits) == 0 {
		return nil
	}
	sort.SliceStable(cluster.Hits, func(left, right int) bool {
		if cluster.Hits[left].FilePath != cluster.Hits[right].FilePath {
			return cluster.Hits[left].FilePath < cluster.Hits[right].FilePath
		}
		return cluster.Hits[left].Line < cluster.Hits[right].Line
	})
	if len(cluster.Hits) > searchLiteralHitLimit {
		cluster.Hits = cluster.Hits[:searchLiteralHitLimit]
	}
	return cluster
}

// searchLiteralRole answers the one question the block exists to answer about a site: does it
// DEFINE the concept or merely pass it? It reports classified=false when neither can be claimed.
//
// The test is structural and needs no language rule: a literal inside a callable body is being used
// (the code that runs when someone calls it), while a literal at type level — an enum constant, a
// constant table, a registry initializer — is part of the declaration of the concept itself.
//
// The last case is the careful one. When no symbol covers the line, the line's own INDENTATION
// decides: an unindented line is a top-level declaration in every language in scope, so EDIT is a
// fact about the text rather than an inference. An indented line is inside something the parser did
// not resolve, and calling that a declaration would send an agent to patch a call site — measured on a
// C file whose function bounds the parser did not cover.
func searchLiteralRole(symbols []SymbolRecord, hit searchNeedleHit) (string, string, bool) {
	if callable, found := smallestSearchSymbolContainingLineWhere(
		symbols, hit.line, func(symbol SymbolRecord) bool { return searchEnclosableSymbolKind(symbol.Kind) },
	); found {
		return searchLiteralSymbolName(callable), SearchLiteralRoleConsumer, true
	}
	if container, found := smallestSearchSymbolContainingLineWhere(symbols, hit.line, nil); found {
		return searchLiteralSymbolName(container), SearchLiteralRoleEdit, true
	}
	if hit.indented {
		return "", "", false
	}
	return "", SearchLiteralRoleEdit, true
}

// searchLiteralHitAlreadyPrinted reports whether an occurrence falls inside source the payload has
// already printed.
func searchLiteralHitAlreadyPrinted(results []SearchResult, hit searchNeedleHit) bool {
	for _, result := range results {
		if result.FilePath != hit.filePath {
			continue
		}
		start, end := result.SnippetStartLine, result.SnippetEndLine
		if start <= 0 || end < start {
			continue
		}
		if start <= hit.line && hit.line <= end {
			return true
		}
	}
	return false
}

// searchLiteralSymbolName prefers the qualified name — `Ops` alone does not say which type — but
// falls back to the bare name, and to nothing rather than to an ID.
func searchLiteralSymbolName(symbol SymbolRecord) string {
	if symbol.QualifiedName != "" {
		return symbol.QualifiedName
	}
	return symbol.Name
}

// fitSearchLiteralCluster shrinks the block until it fits its cap, dropping hits from the END so
// the earliest (path-ordered, hence definition-first in most layouts) survive. It never rewrites
// HitsTotal: the whole point is that the count stays the repository's count while the list shrinks.
func fitSearchLiteralCluster(cluster *SearchLiteralCluster, maxBytes int) *SearchLiteralCluster {
	for len(cluster.Hits) >= 1 {
		if searchLiteralClusterCost(cluster) <= maxBytes {
			return cluster
		}
		cluster.Hits = cluster.Hits[:len(cluster.Hits)-1]
	}
	return nil
}

// searchLiteralClusterCost measures the block on the LARGER of its two wire forms, exactly as the
// container map is measured: a caller pays whichever form it asked for.
func searchLiteralClusterCost(cluster *SearchLiteralCluster) int {
	if cluster == nil {
		return 0
	}
	encoded, err := json.Marshal(cluster)
	if err != nil {
		return 0
	}
	return maxInt(len(encoded), len(RenderSearchLiteralCluster(cluster)))
}

// searchLiteralClusterHeader is the block's label. It states the count so a truncated list can
// never read as the whole picture.
func searchLiteralClusterHeader(cluster *SearchLiteralCluster) string {
	header := fmt.Sprintf("SAME-CONCEPT LITERAL %q — %d in %d file",
		cluster.Literal, cluster.HitsTotal, cluster.FilesTotal)
	if cluster.FilesTotal != 1 {
		header += "s"
	}
	header += " repo-wide"
	if cluster.Unclassified > 0 {
		header += fmt.Sprintf(", %d unparsed", cluster.Unclassified)
	}
	return header + ":"
}

// RenderSearchLiteralCluster renders the block for a text reader. One line per hit, the role last
// so the column an agent scans is the one that says "do I have to open this".
func RenderSearchLiteralCluster(cluster *SearchLiteralCluster) []byte {
	if cluster == nil || len(cluster.Hits) == 0 {
		return nil
	}
	var buffer strings.Builder
	buffer.WriteString(searchLiteralClusterHeader(cluster) + "\n")
	for _, hit := range cluster.Hits {
		fmt.Fprintf(&buffer, "  %s:%d", hit.FilePath, hit.Line)
		if hit.Symbol != "" {
			fmt.Fprintf(&buffer, " %s", hit.Symbol)
		}
		fmt.Fprintf(&buffer, " %s\n", hit.Role)
	}
	return []byte(buffer.String())
}
