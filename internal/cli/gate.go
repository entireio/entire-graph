package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/entireio/entire-graph/internal/gate"
	"github.com/entireio/entire-graph/internal/sem"
)

// ExitCodeError carries a process exit status out of a command that succeeded
// but wants to report a decision. main.go exits with Code and prints nothing:
// the command has already written its own report to stdout, so an error line on
// stderr would be noise, not a diagnosis.
type ExitCodeError struct {
	Code int
	// Reason is the decision behind the code, for callers that inspect the
	// error rather than the exit status.
	Reason string
}

func (e *ExitCodeError) Error() string { return e.Reason }

type gateFlags struct {
	common     commonFlags
	base       string
	head       string
	checkpoint string
	hops       int
	all        bool
}

func parseGateFlags(args []string) (gateFlags, []string, error) {
	parsed := gateFlags{base: "HEAD~1", head: "HEAD", hops: gate.MaxHops}

	common, rest, err := parseCommonFlags(args)
	if err != nil {
		return gateFlags{}, nil, err
	}
	parsed.common = common

	var unknown []string
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--base", "--head", "--checkpoint", "--hops":
			flag := rest[i]
			i++
			if i >= len(rest) {
				return gateFlags{}, nil, fmt.Errorf("%s requires a value", flag)
			}
			if err := parsed.set(flag, rest[i]); err != nil {
				return gateFlags{}, nil, err
			}
		case "--all":
			parsed.all = true
		default:
			unknown = append(unknown, rest[i])
		}
	}
	if parsed.checkpoint != "" && parsed.hasExplicitRange(args) {
		return gateFlags{}, nil, errors.New("--checkpoint cannot be combined with --base or --head")
	}
	return parsed, unknown, nil
}

func (f *gateFlags) set(flag, value string) error {
	switch flag {
	case "--base", "--head":
		if err := validateRevision(flag, value); err != nil {
			return err
		}
		if flag == "--base" {
			f.base = value
		} else {
			f.head = value
		}
	case "--checkpoint":
		f.checkpoint = value
	case "--hops":
		hops, err := strconv.Atoi(value)
		if err != nil || hops < 1 || hops > gate.MaxHops {
			return fmt.Errorf("--hops must be 1 or %d", gate.MaxHops)
		}
		f.hops = hops
	}
	return nil
}

func (f *gateFlags) hasExplicitRange(args []string) bool {
	for _, arg := range args {
		if arg == "--base" || arg == "--head" {
			return true
		}
	}
	return false
}

func runGate(ctx context.Context, opts Options, args []string) error {
	parsed, unknown, err := parseGateFlags(args)
	if err != nil {
		return err
	}
	if len(unknown) != 0 {
		return unexpectedArgumentsError("gate", opts.Version, unknown)
	}

	repo, err := resolveRepo(ctx, opts.Env, parsed.common.Repo)
	if err != nil {
		return err
	}

	report, err := collectGateReport(ctx, repo, opts.Version, parsed)
	if err != nil {
		return err
	}

	if parsed.common.JSON {
		if err := gate.WriteJSON(opts.Stdout, report); err != nil {
			return err
		}
	} else {
		gate.WriteText(opts.Stdout, report, parsed.all)
	}

	if code := report.Verdict.ExitCode(); code != 0 {
		return &ExitCodeError{Code: code, Reason: string(report.Verdict)}
	}
	return nil
}

// collectGateReport is the only impure step: it reads the semantic diff and the
// graph, then hands records to the pure signal and verdict layers. Keeping every
// read here is what lets those layers be tested without git or tree-sitter.
func collectGateReport(ctx context.Context, repo, version string, flags gateFlags) (gate.Report, error) {
	result, err := analyzeRange(ctx, repo, flags)
	if err != nil {
		return gate.Report{}, err
	}

	entities := changedEntities(result)
	report := gate.Report{
		Base:       result.Base,
		Head:       result.Head,
		Checkpoint: result.Checkpoint,
		Entities:   entities,
	}
	for _, warning := range result.Warnings {
		report.Warnings = append(report.Warnings, gateWarning(warning))
	}

	// The full profile is required, not preferred: fast omits USES_TYPE,
	// PARAM_TYPE, RETURNS_TYPE and DATA_FLOWS, which are four of the nine edge
	// types the blast radius walks.
	snapshot, err := sem.BuildProviderSnapshotWithOptions(ctx, repo, version,
		sem.ProviderSnapshotOptions{Profile: sem.ProfileFull})
	if err != nil {
		// A graph we could not build is a missing dimension, not a failing
		// change. Report what the diff alone can say and let the verdict
		// degrade to unusable rather than accusing the author.
		report.Warnings = append(report.Warnings, "graph unavailable: "+err.Error())
		report.Verdict = gate.Decide(report.Entities, nil, gate.Availability{})
		report.ExitCode = report.Verdict.ExitCode()
		return report, nil
	}

	index := gate.NewIndex(projectSymbols(snapshot), projectRelations(snapshot))
	risk := gate.Risk(report.Entities, index, flags.hops)
	gate.ResolveCoverage(report.Entities, index, snapshotHasTests(snapshot))
	coverage := gate.Coverage(report.Entities)

	report.Findings = append(risk, coverage...)
	report.Available = gate.Availability{Risk: true, Coverage: snapshotHasTests(snapshot)}
	report.Verdict = gate.Decide(report.Entities, report.Findings, report.Available)
	report.ExitCode = report.Verdict.ExitCode()
	report.VerifyCommand = verifyCommand(repo)
	return report, nil
}

// gateWarning renders a provider warning as one line. The completeness effect
// is kept because it is the part that tells a reader whether the report they
// are holding is whole.
func gateWarning(w sem.ProviderWarning) string {
	line := w.Code
	if w.FilePath != "" {
		line += " " + w.FilePath
	}
	if w.EffectOnCompleteness != "" {
		line += " (" + w.EffectOnCompleteness + ")"
	}
	return line
}

func analyzeRange(ctx context.Context, repo string, flags gateFlags) (sem.Result, error) {
	if flags.checkpoint != "" {
		return sem.AnalyzeCheckpoint(ctx, repo, flags.checkpoint)
	}
	return sem.AnalyzeGitRange(ctx, repo, flags.base, flags.head, nil)
}

func changedEntities(result sem.Result) []gate.ChangedEntity {
	var entities []gate.ChangedEntity
	for _, file := range result.Files {
		for _, change := range file.Changes {
			entities = append(entities, gate.ChangedEntity{
				Anchor: gate.Anchor{
					Name: change.Name,
					Path: file.Path,
					Line: changeLine(change),
				},
				Kind:       change.Kind,
				ChangeType: gate.ChangeType(change.Type),
				// Dependents is left at zero here on purpose: sem's count comes
				// from an identifier scan, while Risk recomputes it from graph
				// edges. Two numbers for one thing would be worse than one.
			})
		}
	}
	// One total order over the change set. sem walks files and changes in git's
	// order, which is stable, but Gate's own output must not depend on that
	// staying true — and the review order and JSON payload are both compared
	// run-to-run as evidence of determinism.
	sort.Slice(entities, func(i, j int) bool {
		a, b := entities[i], entities[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Line < b.Line
	})
	return entities
}

// changeLine prefers the post-change location, because that is where a reviewer
// will look. A removed entity has no after-line, so its before-line is the only
// address it has.
func changeLine(change sem.EntityChange) int {
	if change.AfterStartLine > 0 {
		return change.AfterStartLine
	}
	return change.BeforeStartLine
}

func projectSymbols(snapshot sem.ProviderSnapshot) []gate.Symbol {
	symbols := make([]gate.Symbol, 0, len(snapshot.Symbols))
	for _, s := range snapshot.Symbols {
		symbols = append(symbols, gate.Symbol{
			ID: s.ID, Name: s.Name, Path: s.FilePath, Line: s.StartLine, Kind: s.Kind,
		})
	}
	return symbols
}

func projectRelations(snapshot sem.ProviderSnapshot) []gate.Relation {
	relations := make([]gate.Relation, 0, len(snapshot.Relations))
	for _, r := range snapshot.Relations {
		relations = append(relations, gate.Relation{FromID: r.FromID, ToID: r.ToID, Type: r.Type})
	}
	return relations
}

func snapshotHasTests(snapshot sem.ProviderSnapshot) bool {
	for _, file := range snapshot.Files {
		if gate.IsTestPath(file.Path) {
			return true
		}
	}
	return false
}

// verifyCommand derives the narrowest runnable test invocation for the
// repository. It is deliberately conservative: a wrong command costs more than
// no command, so an unrecognised project reports nothing rather than a guess.
//
// The manifest list is ordered, not a map: Gate's central claim is that two
// runs of the same commit produce identical bytes, and Go randomises map
// iteration, so a repository holding two manifests would answer differently
// run to run.
func verifyCommand(repo string) string {
	manifests := []struct{ file, command string }{
		{"go.mod", "go test ./..."},
		{"Cargo.toml", "cargo test"},
		{"pyproject.toml", "pytest"},
		{"package.json", "npm test"},
	}
	for _, m := range manifests {
		if _, err := os.Stat(filepath.Join(repo, m.file)); err == nil {
			return m.command
		}
	}
	return ""
}
