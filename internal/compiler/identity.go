// Package compiler implements optional source-bound compiler evidence through
// a bounded stdio client and the tested Linux network-denying process boundary.
package compiler

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"sort"
	"strconv"
)

type Input struct {
	Path    string
	Digest  string
	Present bool
}
type BuildContext struct {
	OperationID      string
	Inputs           []Input
	Configuration    []string // ordered; callers include tags, GOOS/GOARCH, cgo and manifest identities
	Packages         []string // unordered set
	AdapterVersion   string
	ServerVersion    string
	ToolchainVersion string
}

func hashField(h hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(value))
}

func (build BuildContext) ID() (string, error) {
	if !validDigest(build.OperationID) || build.AdapterVersion == "" || build.ServerVersion == "" || build.ToolchainVersion == "" {
		return "", errors.New("incomplete compiler context")
	}
	h := sha256.New()
	hashField(h, "compiler-context-v1")
	for _, field := range []string{build.OperationID, build.AdapterVersion, build.ServerVersion, build.ToolchainVersion} {
		hashField(h, field)
	}
	inputs := append([]Input(nil), build.Inputs...)
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Path < inputs[j].Path })
	hashField(h, "inputs")
	hashField(h, strconv.Itoa(len(inputs)))
	for i, input := range inputs {
		if input.Path == "" || (i > 0 && inputs[i-1].Path == input.Path) || input.Present && !validDigest(input.Digest) || !input.Present && input.Digest != "" {
			return "", errors.New("invalid compiler input identity")
		}
		hashField(h, input.Path)
		if input.Present {
			hashField(h, "present")
		} else {
			hashField(h, "missing")
		}
		hashField(h, input.Digest)
	}
	hashField(h, "configuration")
	hashField(h, strconv.Itoa(len(build.Configuration)))
	for _, value := range build.Configuration {
		hashField(h, value)
	}
	hashField(h, "packages")
	packages := append([]string(nil), build.Packages...)
	sort.Strings(packages)
	unique := packages[:0]
	for _, value := range packages {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	packages = unique
	hashField(h, strconv.Itoa(len(packages)))
	for i, value := range packages {
		if value == "" {
			return "", errors.New("empty analyzed package")
		}
		if i == 0 || packages[i-1] != value {
			hashField(h, value)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validDigest(value string) bool {
	bytes, err := hex.DecodeString(value)
	return err == nil && len(bytes) == sha256.Size && value == hex.EncodeToString(bytes)
}

func ContentDigest(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

type TargetCategory string

const (
	DirectDeclaration       TargetCategory = "direct_declaration"
	ImplementationCandidate TargetCategory = "implementation_candidate"
)

type Site struct {
	Path      string `json:"path"`
	Digest    string `json:"digest"`
	StartByte int    `json:"start_byte"`
	EndByte   int    `json:"end_byte"`
}
type Evidence struct {
	ContextID      string         `json:"context_id"`
	BackendVersion string         `json:"backend_version"`
	Category       TargetCategory `json:"category"`
	QueryKind      string         `json:"query_kind"`
	Caller         Site           `json:"caller"`
	Target         Site           `json:"target"`
	TargetSymbolID string         `json:"target_symbol_id"`
}

// ValidateEvidence validates input binding only. A semantic adapter must also
// match the exact target declaration range/kind/name to TargetSymbolID before
// reconciliation. Passing this validator is not a compiler correctness oracle.
func ValidateEvidence(e Evidence, contextID, backendVersion string, sources map[string]string) error {
	if !validDigest(contextID) || e.ContextID != contextID || backendVersion == "" || e.BackendVersion != backendVersion {
		return errors.New("stale compiler evidence")
	}
	if e.TargetSymbolID == "" {
		return errors.New("unmapped compiler target")
	}
	if e.Category == DirectDeclaration {
		if e.QueryKind != "textDocument/definition" {
			return errors.New("direct target requires definition evidence")
		}
	} else if e.Category == ImplementationCandidate {
		if e.QueryKind != "textDocument/implementation" {
			return errors.New("candidate requires implementation evidence")
		}
	} else {
		return errors.New("unknown compiler target category")
	}
	for _, site := range []Site{e.Caller, e.Target} {
		content, ok := sources[site.Path]
		if !ok || ContentDigest(content) != site.Digest || site.StartByte < 0 || site.EndByte <= site.StartByte || site.EndByte > len(content) {
			return errors.New("compiler evidence does not match captured source")
		}
		if _, err := PositionAt(content, site.StartByte); err != nil {
			return err
		}
		if _, err := PositionAt(content, site.EndByte); err != nil {
			return err
		}
	}
	return nil
}
