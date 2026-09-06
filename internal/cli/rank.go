package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/entireio/entire-graph/internal/gitutil"
	"github.com/entireio/entire-graph/internal/rank"
	"github.com/entireio/entire-graph/internal/sem"
	"github.com/entireio/entire-graph/internal/termsafe"
)

// runRank dispatches `entire graph rank <subcommand>`. It is the CLI-facing
// half of the Hacker House developer-ranking feature: everything that
// actually turns commits into scores lives in internal/rank, which does no
// I/O of its own — this file's only job is turning flags into the sem.Result
// (semantic diff) and sem.ProviderSnapshot (relation graph) that package
// already produces for `diff`/`commit` and `impact`/`neighbors`, and handing
// them to rank.AnalyzeCommit/AggregateDeveloper.
func runRank(ctx context.Context, opts Options, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("rank requires a subcommand: demo, commit, or developer")
	}
	switch args[0] {
	case "demo":
		return runRankDemo(ctx, opts, args[1:])
	case "commit":
		return runRankCommit(ctx, opts, args[1:])
	case "developer":
		return runRankDeveloper(ctx, opts, args[1:])
	case "leaderboard":
		return runRankLeaderboard(ctx, opts, args[1:])
	default:
		return fmt.Errorf("unknown rank subcommand %q (want demo, commit, developer, or leaderboard)", args[0])
	}
}

func parseRankFormat(args []string) (format string, rest []string, err error) {
	format = "text"
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--format" {
			i++
			if i >= len(args) {
				return "", nil, fmt.Errorf("--format requires a value")
			}
			format = args[i]
			continue
		}
		rest = append(rest, args[i])
	}
	if format != "text" && format != "json" {
		return "", nil, fmt.Errorf("rank --format must be text or json, got %q", format)
	}
	return format, rest, nil
}

// runRankDemo runs the deterministic three-developer fixture (see
// rank.Demo): no repository required, so it is always available as a demo
// path even against an empty checkout.
func runRankDemo(ctx context.Context, opts Options, args []string) error {
	format, rest, err := parseRankFormat(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("rank demo received unexpected argument %q", rest[0])
	}
	profiles := rank.Demo(rank.DefaultWeights())
	sortProfilesByFinalScoreDescending(profiles)
	return writeRankProfiles(opts.Stdout, profiles, format)
}

// rankSnapshotFlags are the index/caching flags `rank commit` and `rank
// developer` share with `impact`/`neighbors`: same meaning, same defaults
// where it makes sense to. One difference is deliberate -- Worktree defaults
// to FALSE here (committed tree), not true. impact/neighbors default to the
// working tree because they answer "what does my code look like right now";
// rank analyzes ALREADY-COMMITTED history, which is exactly the case
// LoadOrBuildProviderSnapshot can cache, and a full first-time build on a
// large real-world repo is minutes, not seconds -- see runRankDeveloper,
// which builds the snapshot once and reuses it across every --commit. --worktree
// opts back into the impact/neighbors default when that is what is wanted.
type rankSnapshotFlags struct {
	CacheDir     string
	DisableCache bool
	Worktree     bool
	// Profile is the raw --profile value ("" defaults to full, matching every
	// other graph-query command). fast drops evidence collection and deep call
	// resolution (still resolves CALLS/CONSTRUCTS/HANDLES_ROUTE/HANDLES_TOOL,
	// but skips IMPLEMENTS/EXTENDS/INHERITS/USES_TYPE/PARAM_TYPE/RETURNS_TYPE
	// entirely -- see resolveProfile in provider.go) in exchange for being
	// several times faster on a large repository. That trade-off is real: a
	// fast-profile CommitAnalysis will report 0 InterfacesAffected/type
	// consumers even where full would find some. Structural/dependent/route
	// evidence -- the two heaviest-weighted components -- are unaffected.
	Profile string
}

func parseRankSnapshotFlag(args []string, index int, flags *rankSnapshotFlags) (consumed bool, next int, err error) {
	switch args[index] {
	case "--cache-dir":
		index++
		if index >= len(args) {
			return true, index, fmt.Errorf("--cache-dir requires a value")
		}
		flags.CacheDir = args[index]
		return true, index, nil
	case "--no-cache":
		flags.DisableCache = true
		return true, index, nil
	case "--worktree":
		flags.Worktree = true
		return true, index, nil
	case "--head":
		flags.Worktree = false
		return true, index, nil
	case "--profile":
		index++
		if index >= len(args) {
			return true, index, fmt.Errorf("--profile requires a value")
		}
		flags.Profile = args[index]
		return true, index, nil
	default:
		return false, index, nil
	}
}

func loadRankSnapshot(ctx context.Context, repo, version string, opts Options, flags rankSnapshotFlags) (sem.ProviderSnapshot, error) {
	profile, err := parseProfile(flags.Profile)
	if err != nil {
		return sem.ProviderSnapshot{}, err
	}
	cacheDir := resolveCacheDir(flags.CacheDir, opts.Env.PluginDataDir)
	snapshot, _, err := sem.LoadOrBuildProviderSnapshot(ctx, repo, version, sem.ProviderSnapshotOptions{
		NoNetwork: true,
		Worktree:  flags.Worktree,
		Profile:   profile,
	}, cacheDir, flags.DisableCache)
	return snapshot, err
}

// runRankCommit analyzes one commit/PR and prints its CommitImpactScore --
// the "Analyze Commit" capability (Section 8): "why did this commit
// contribute to the developer's ranking?"
func runRankCommit(ctx context.Context, opts Options, args []string) error {
	var repoFlag, format string
	format = "text"
	var rev string
	var snapshotFlags rankSnapshotFlags
	for i := 0; i < len(args); i++ {
		if consumed, next, err := parseRankSnapshotFlag(args, i, &snapshotFlags); consumed {
			if err != nil {
				return err
			}
			i = next
			continue
		}
		switch args[i] {
		case "--repo":
			i++
			if i >= len(args) {
				return fmt.Errorf("--repo requires a value")
			}
			repoFlag = args[i]
		case "--format":
			i++
			if i >= len(args) {
				return fmt.Errorf("--format requires a value")
			}
			format = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("rank commit received unexpected argument %q", args[i])
			}
			if rev != "" {
				return fmt.Errorf("rank commit accepts at most one revision")
			}
			rev = args[i]
		}
	}
	if format != "text" && format != "json" {
		return fmt.Errorf("rank --format must be text or json, got %q", format)
	}
	if rev == "" {
		rev = "HEAD"
	}
	if err := validateRevision("rank commit", rev); err != nil {
		return err
	}

	repo, err := resolveRepo(ctx, opts.Env, repoFlag)
	if err != nil {
		return err
	}
	snapshot, err := loadRankSnapshot(ctx, repo, opts.Version, opts, snapshotFlags)
	if err != nil {
		return err
	}
	analysis, err := analyzeOneCommit(ctx, repo, rev, &snapshot)
	if err != nil {
		return err
	}

	if format == "json" {
		encoder := json.NewEncoder(termsafe.NewJSONWriter(opts.Stdout))
		encoder.SetEscapeHTML(false)
		return encoder.Encode(analysis)
	}
	fmt.Fprint(opts.Stdout, analysis.Explain())
	return nil
}

// runRankDeveloper analyzes a developer's chosen commits against one
// repository and combines them with the preserved GitHub reach formula
// (Section 3) into a FinalScore.
func runRankDeveloper(ctx context.Context, opts Options, args []string) error {
	var repoFlag, format, username string
	format = "text"
	stars, userPRs, totalPRs := -1, -1, -1
	var revs []string
	var snapshotFlags rankSnapshotFlags
	for i := 0; i < len(args); i++ {
		if consumed, nextIndex, err := parseRankSnapshotFlag(args, i, &snapshotFlags); consumed {
			if err != nil {
				return err
			}
			i = nextIndex
			continue
		}
		next := func() (string, error) {
			i++
			if i >= len(args) {
				return "", fmt.Errorf("%s requires a value", args[i-1])
			}
			return args[i], nil
		}
		var err error
		switch args[i] {
		case "--repo":
			repoFlag, err = next()
		case "--format":
			format, err = next()
		case "--username":
			username, err = next()
		case "--stars":
			stars, i, err = rankPositiveIntFlag(args, i)
		case "--user-prs":
			userPRs, i, err = rankPositiveIntFlag(args, i)
		case "--total-prs":
			totalPRs, i, err = rankPositiveIntFlag(args, i)
		case "--commit":
			var value string
			value, err = next()
			if err == nil {
				revs = append(revs, value)
			}
		default:
			err = fmt.Errorf("rank developer received unexpected argument %q", args[i])
		}
		if err != nil {
			return err
		}
	}
	if format != "text" && format != "json" {
		return fmt.Errorf("rank --format must be text or json, got %q", format)
	}
	if username == "" {
		return fmt.Errorf("rank developer requires --username")
	}
	if stars < 0 || userPRs < 0 || totalPRs < 0 {
		return fmt.Errorf("rank developer requires --stars, --user-prs, and --total-prs")
	}
	if len(revs) == 0 {
		return fmt.Errorf("rank developer requires at least one --commit")
	}

	repo, err := resolveRepo(ctx, opts.Env, repoFlag)
	if err != nil {
		return err
	}

	// One relation graph is built (or loaded from the on-disk cache -- see
	// loadRankSnapshot) and reused across every --commit: the graph reflects
	// the CURRENT repository state (the same thing `impact` and `neighbors`
	// query), so building it once per invocation instead of once per commit
	// is both correct and the difference between one slow index build and N
	// of them.
	snapshot, err := loadRankSnapshot(ctx, repo, opts.Version, opts, snapshotFlags)
	if err != nil {
		return err
	}

	commits := make([]rank.CommitAnalysis, 0, len(revs))
	for _, rev := range revs {
		analysis, err := analyzeOneCommit(ctx, repo, rev, &snapshot)
		if err != nil {
			return err
		}
		commits = append(commits, analysis)
	}

	profile := rank.AggregateDeveloper(username, stars, userPRs, totalPRs, commits, rank.DefaultWeights(), time.Now())
	if format == "json" {
		encoder := json.NewEncoder(termsafe.NewJSONWriter(opts.Stdout))
		encoder.SetEscapeHTML(false)
		return encoder.Encode(profile)
	}
	fmt.Fprint(opts.Stdout, profile.Explain())
	return nil
}

// rankRosterEntry is one line of a --roster file: a developer plus the
// GitHub-reach inputs and the commits/PRs to analyze for them. GitHub is the
// source of truth for stars/PR counts and for "which commits are this
// developer's merged PRs" -- this package does not call the GitHub API, so a
// roster is how that external fact enters the pipeline (see package rank's
// doc comment: Entire is the evidence provider, not the source of who merged
// what).
type rankRosterEntry struct {
	Username string   `json:"username"`
	Stars    int      `json:"stars"`
	UserPRs  int      `json:"user_prs"`
	TotalPRs int      `json:"total_prs"`
	Commits  []string `json:"commits"`
}

// runRankLeaderboard scores every developer in a --roster file against ONE
// repository, sharing a single relation-graph build (see loadRankSnapshot)
// across every developer and every one of their commits -- the multi-
// developer analogue of `rank developer`, for "show me contributors and
// their score" on a real repository without a GitHub API integration.
func runRankLeaderboard(ctx context.Context, opts Options, args []string) error {
	var repoFlag, format, rosterPath string
	format = "text"
	var snapshotFlags rankSnapshotFlags
	for i := 0; i < len(args); i++ {
		if consumed, next, err := parseRankSnapshotFlag(args, i, &snapshotFlags); consumed {
			if err != nil {
				return err
			}
			i = next
			continue
		}
		next := func() (string, error) {
			i++
			if i >= len(args) {
				return "", fmt.Errorf("%s requires a value", args[i-1])
			}
			return args[i], nil
		}
		var err error
		switch args[i] {
		case "--repo":
			repoFlag, err = next()
		case "--format":
			format, err = next()
		case "--roster":
			rosterPath, err = next()
		default:
			err = fmt.Errorf("rank leaderboard received unexpected argument %q", args[i])
		}
		if err != nil {
			return err
		}
	}
	if format != "text" && format != "json" {
		return fmt.Errorf("rank --format must be text or json, got %q", format)
	}
	if rosterPath == "" {
		return fmt.Errorf("rank leaderboard requires --roster <path.json>")
	}

	rosterBytes, err := os.ReadFile(rosterPath)
	if err != nil {
		return fmt.Errorf("read --roster: %w", err)
	}
	var roster []rankRosterEntry
	if err := json.Unmarshal(rosterBytes, &roster); err != nil {
		return fmt.Errorf("parse --roster %s: %w", rosterPath, err)
	}
	if len(roster) == 0 {
		return fmt.Errorf("--roster %s lists no developers", rosterPath)
	}

	repo, err := resolveRepo(ctx, opts.Env, repoFlag)
	if err != nil {
		return err
	}
	snapshot, err := loadRankSnapshot(ctx, repo, opts.Version, opts, snapshotFlags)
	if err != nil {
		return err
	}

	profiles := make([]rank.DeveloperProfile, 0, len(roster))
	now := time.Now()
	for _, entry := range roster {
		if entry.Username == "" {
			return fmt.Errorf("--roster %s has an entry with no username", rosterPath)
		}
		commits := make([]rank.CommitAnalysis, 0, len(entry.Commits))
		for _, rev := range entry.Commits {
			analysis, err := analyzeOneCommit(ctx, repo, rev, &snapshot)
			if err != nil {
				return fmt.Errorf("%s: %w", entry.Username, err)
			}
			commits = append(commits, analysis)
		}
		profiles = append(profiles, rank.AggregateDeveloper(
			entry.Username, entry.Stars, entry.UserPRs, entry.TotalPRs, commits, rank.DefaultWeights(), now,
		))
	}

	sortProfilesByFinalScoreDescending(profiles)
	return writeRankProfiles(opts.Stdout, profiles, format)
}

// rankPositiveIntFlag parses one `--flag N` pair, reusing the same shape
// searchPositiveIntFlag elsewhere in this package uses.
func rankPositiveIntFlag(args []string, index int) (int, int, error) {
	name := args[index]
	index++
	if index >= len(args) {
		return 0, index, fmt.Errorf("%s requires a value", name)
	}
	value, err := strconv.Atoi(args[index])
	if err != nil || value < 0 {
		return 0, index, fmt.Errorf("%s must be a non-negative integer, got %q", name, args[index])
	}
	return value, index, nil
}

// analyzeOneCommit resolves one revision's first-parent base, runs the
// existing semantic-diff analysis (sem.AnalyzeGitRangeWithOptions -- the same
// engine `diff`/`commit` use) for that range, and folds it together with the
// (already built/cached -- see loadRankSnapshot) relation graph into a
// rank.CommitAnalysis.
func analyzeOneCommit(ctx context.Context, repo, rev string, snapshot *sem.ProviderSnapshot) (rank.CommitAnalysis, error) {
	if err := sem.EnsureGitMetadataSafeForSubprocess(repo); err != nil {
		return rank.CommitAnalysis{}, err
	}
	resolvedRev, err := gitutil.RevParse(ctx, repo, rev)
	if err != nil {
		return rank.CommitAnalysis{}, err
	}
	base, err := gitutil.FirstParent(ctx, repo, resolvedRev)
	if err != nil {
		return rank.CommitAnalysis{}, err
	}
	diff, err := sem.AnalyzeGitRangeWithOptions(ctx, repo, base, resolvedRev, nil, sem.AnalyzeOptions{})
	if err != nil {
		return rank.CommitAnalysis{}, err
	}
	// Best-effort: an unresolvable timestamp (e.g. a shallow clone missing the
	// commit's author-date encoding) degrades recency weighting to "unknown",
	// not a hard failure -- the analysis itself does not depend on it.
	timestamp, _ := gitutil.CommitTimestamp(ctx, repo, resolvedRev)

	return rank.AnalyzeCommit(resolvedRev, timestamp, diff, *snapshot, rank.DefaultWeights()), nil
}

func sortProfilesByFinalScoreDescending(profiles []rank.DeveloperProfile) {
	sort.SliceStable(profiles, func(i, j int) bool {
		return profiles[i].FinalScore > profiles[j].FinalScore
	})
}

func writeRankProfiles(out io.Writer, profiles []rank.DeveloperProfile, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(termsafe.NewJSONWriter(out))
		encoder.SetEscapeHTML(false)
		return encoder.Encode(profiles)
	}
	writeRankTable(out, profiles)
	for _, p := range profiles {
		fmt.Fprintln(out)
		fmt.Fprint(out, p.Explain())
	}
	return nil
}

// writeRankTable prints the leaderboard line the product spec's Section 11
// example shows: rank, username, FinalScore, a semantic-impact label, and how
// many commits back it. Detailed per-developer evidence follows separately
// (writeRankProfiles appends each profile's Explain()).
func writeRankTable(out io.Writer, profiles []rank.DeveloperProfile) {
	fmt.Fprintln(out, "Developer Ranking")
	fmt.Fprintln(out)
	for i, p := range profiles {
		impact := "Low impact"
		switch {
		case p.EvidenceState == sem.EvidenceRequiresVerification:
			impact = "Unverified"
		case p.SemanticImpact == "high":
			impact = "High impact"
		case p.SemanticImpact == "medium":
			impact = "Medium impact"
		}
		fmt.Fprintf(out, "%d. %-12s %5.1f   %-12s %d commit%s\n",
			i+1, p.Username, p.FinalScore, impact, p.CommitsAnalyzed, pluralSuffix(p.CommitsAnalyzed))
	}
}
