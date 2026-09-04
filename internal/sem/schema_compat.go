package sem

import (
	"fmt"
	"strings"
)

// ADR 0001 ratifies the GA schema contract this provider emits and, in the same
// breath, the rules for READING it back:
//
//	1. Major = compatibility boundary. Consumers refuse an unknown major version.
//	3. Tolerant readers required. Consumers ignore unknown fields within a
//	   supported major, and warn (not fail) when they see a newer supported-major
//	   minor, since additive facts may have been skipped.
//
// entire-graph is itself such a consumer: `snapshot-query` reads compact
// snapshots off disk, and the bench preflight round-trips them. Those are the
// only places this build ingests a serialized graph it did not just produce, so
// they are where the rule has to be enforced. Nothing else does it — the compact
// envelope version (CompactSnapshotFormatVersion) covers the ARRAY ENCODING, not
// the record schema carried in the header, and the two move independently.
//
// The parse is deliberately strict about the version STRING as well. A header
// with no schema_version, or one that is not major.minor, is not a tolerable
// older artifact — it is a version this reader cannot place on either side of
// the compatibility boundary, so it is refused for the same reason an unknown
// major is.

// schemaMajorMinor splits a major.minor schema version. Trailing components are
// rejected rather than ignored: the contract names exactly two, and silently
// accepting a third would let a "1.1.x" line through unclassified.
func schemaMajorMinor(version string) (string, string, error) {
	if version == "" {
		return "", "", fmt.Errorf("schema version is missing")
	}
	parts := strings.Split(version, ".")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("schema version %q is not major.minor", version)
	}
	major, ok := normalizeSchemaNumber(parts[0])
	if !ok {
		return "", "", fmt.Errorf("schema version %q has an unreadable major", version)
	}
	minor, ok := normalizeSchemaNumber(parts[1])
	if !ok {
		return "", "", fmt.Errorf("schema version %q has an unreadable minor", version)
	}
	return major, minor, nil
}

// normalizeSchemaNumber validates a nonnegative decimal component and returns
// a canonical representation that can be compared without converting through
// a platform-sized integer. A leading '+' remains accepted solely for
// compatibility with the previous strconv.Atoi parser. Atoi made whether a
// valid 1.x minor was readable depend on the reader's word size.
func normalizeSchemaNumber(value string) (string, bool) {
	if strings.HasPrefix(value, "+") {
		value = value[1:]
	}
	if value == "" {
		return "", false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return "", false
		}
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		return "0", true
	}
	return value, true
}

func compareSchemaNumbers(left, right string) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
}

// CheckReadableSchemaVersion applies ADR 0001 to a schema version read off a
// persisted artifact. It returns an error when this build cannot read the
// artifact at all (unknown or unreadable major), and otherwise reports whether
// the artifact was written under a NEWER minor of this major — readable by the
// additive-only rule, but missing whatever facts that minor added, which the
// caller is expected to surface as a warning rather than an error.
func CheckReadableSchemaVersion(declared string) (newerMinor bool, err error) {
	major, minor, err := schemaMajorMinor(declared)
	if err != nil {
		return false, err
	}
	wantMajor, wantMinor, err := schemaMajorMinor(SchemaVersion)
	if err != nil {
		// The package constant is malformed; that is a build defect, not an
		// artifact defect, and must not be reported as the artifact's fault.
		return false, fmt.Errorf("provider schema version %q is malformed: %w", SchemaVersion, err)
	}
	if major != wantMajor {
		return false, fmt.Errorf(
			"unsupported schema version %q: this build reads major %s (%s) and a different major is not backward compatible",
			declared, wantMajor, SchemaVersion,
		)
	}
	return compareSchemaNumbers(minor, wantMinor) > 0, nil
}

// newerSchemaMinorWarning is the ADR-mandated warning for an artifact from a
// newer minor of a readable major: the known projection parses, but additive
// fields or record kinds from that minor are absent from what this build
// understood.
func newerSchemaMinorWarning(declared string) ProviderWarning {
	return ProviderWarning{
		Code:                 "W_NEWER_SCHEMA_MINOR",
		Severity:             "warning",
		EffectOnCompleteness: "additive fields or record kinds introduced after this build's schema were not read",
		Detail: fmt.Sprintf(
			"artifact declares schema %s; this build reads %s",
			declared, SchemaVersion,
		),
	}
}
