package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/entireio/entire-graph/internal/sem"
)

type snapshotQueryFlags struct {
	Input    string
	Symbol   string
	From     string
	Relation string
	Format   string
}

func runSnapshotQuery(opts Options, args []string) error {
	flags, err := parseSnapshotQueryFlags(args)
	if err != nil {
		return err
	}
	file, err := os.Open(flags.Input)
	if err != nil {
		return fmt.Errorf("open compact snapshot: %w", err)
	}
	defer file.Close()
	index, err := sem.LoadCompactSnapshot(file)
	if err != nil {
		return fmt.Errorf("load compact snapshot: %w", err)
	}
	// ADR 0001: a newer minor of a readable major is served, not refused, but the
	// caller has to learn that additive facts were skipped. stdout is the record
	// stream a consumer parses, so the notice goes to stderr.
	// The write is checked because ADR 0001 clause 3 makes this warning
	// MANDATORY for a reader: if it cannot be delivered, serving the records as
	// though the consumer had been told is the failure the clause exists to
	// prevent. A dropped warning is silent by construction — nothing downstream
	// can tell it was owed one — so the only place it can be caught is here.
	for _, warning := range index.SchemaWarnings {
		if _, err := fmt.Fprintf(opts.Stderr, "graph warning code=%s detail=%s\n", warning.Code, warning.Detail); err != nil {
			return fmt.Errorf("write schema compatibility warning: %w", err)
		}
	}
	result := index.Query(sem.CompactSnapshotQuery{Symbol: flags.Symbol, FromID: flags.From, Relation: flags.Relation})
	encoder := json.NewEncoder(opts.Stdout)
	encoder.SetEscapeHTML(false)
	for _, symbol := range result.Symbols {
		if err := encoder.Encode(symbol); err != nil {
			return err
		}
	}
	for _, relation := range result.Relations {
		if err := encoder.Encode(relation); err != nil {
			return err
		}
	}
	return nil
}

func parseSnapshotQueryFlags(args []string) (snapshotQueryFlags, error) {
	flags := snapshotQueryFlags{Format: "ndjson"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--input":
			i++
			if i >= len(args) {
				return flags, errors.New("--input requires a value")
			}
			flags.Input = args[i]
		case "--symbol":
			i++
			if i >= len(args) {
				return flags, errors.New("--symbol requires a value")
			}
			flags.Symbol = args[i]
		case "--from":
			i++
			if i >= len(args) {
				return flags, errors.New("--from requires a value")
			}
			flags.From = args[i]
		case "--relation":
			i++
			if i >= len(args) {
				return flags, errors.New("--relation requires a value")
			}
			flags.Relation = strings.ToUpper(args[i])
		case "--format":
			i++
			if i >= len(args) {
				return flags, errors.New("--format requires a value")
			}
			flags.Format = args[i]
		default:
			return flags, fmt.Errorf("snapshot-query received unexpected argument %q", args[i])
		}
	}
	if flags.Input == "" {
		return flags, errors.New("snapshot-query requires --input")
	}
	if flags.Symbol == "" && flags.From == "" {
		return flags, errors.New("snapshot-query requires at least --symbol or --from")
	}
	if flags.Relation != "" && flags.From == "" {
		return flags, errors.New("--relation requires --from")
	}
	if flags.Format != "ndjson" {
		return flags, errors.New("snapshot-query requires --format ndjson")
	}
	return flags, nil
}
