// Package intent owns repository-authored GPS specifications and bindings.
package intent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/entireio/entire-graph/internal/gitutil"
	"gopkg.in/yaml.v3"
)

const Root = ".entire/graph"

const maxDocumentBytes = 1 << 20

type Policy struct {
	Version     int    `yaml:"version" json:"version"`
	SpecsRoot   string `yaml:"specs_root" json:"specs_root"`
	AnchorsRoot string `yaml:"anchors_root" json:"anchors_root"`
}

type Requirement struct {
	ID          string `yaml:"id" json:"id"`
	Description string `yaml:"description" json:"description"`
}
type Acceptance struct {
	ID          string `yaml:"id" json:"id"`
	Requirement string `yaml:"requirement" json:"requirement"`
	Description string `yaml:"description" json:"description"`
}
type AnchorRef struct {
	ID          string `yaml:"id" json:"id"`
	Requirement string `yaml:"requirement" json:"requirement"`
}
type TestRef struct {
	ID         string `yaml:"id" json:"id"`
	Acceptance string `yaml:"acceptance" json:"acceptance"`
	Selector   struct {
		Name string `yaml:"name" json:"name"`
	} `yaml:"selector" json:"selector"`
}
type Relationship struct {
	Type   string `yaml:"type" json:"type"`
	Target string `yaml:"target" json:"target"`
}
type Decision struct {
	Version  int      `yaml:"version" json:"version"`
	ID       string   `yaml:"id" json:"id"`
	Title    string   `yaml:"title" json:"title"`
	Decision string   `yaml:"decision" json:"decision"`
	Affects  []string `yaml:"affects" json:"affects"`
	Anchors  []string `yaml:"anchors" json:"anchors"`
	Path     string   `yaml:"-" json:"path"`
}
type Spec struct {
	Version       int            `yaml:"version" json:"version"`
	ID            string         `yaml:"id" json:"id"`
	Title         string         `yaml:"title" json:"title"`
	Intent        string         `yaml:"intent" json:"intent"`
	Status        string         `yaml:"status,omitempty" json:"status,omitempty"`
	Requirements  []Requirement  `yaml:"requirements" json:"requirements"`
	Acceptance    []Acceptance   `yaml:"acceptance" json:"acceptance"`
	Anchors       []AnchorRef    `yaml:"anchors" json:"anchors"`
	Tests         []TestRef      `yaml:"tests" json:"tests"`
	Relationships []Relationship `yaml:"relationships,omitempty" json:"relationships,omitempty"`
	Path          string         `yaml:"-" json:"path"`
}

type Baseline struct {
	Version       int    `yaml:"version" json:"version"`
	SignatureHash string `yaml:"signature_hash" json:"signature_hash"`
	ContainerID   string `yaml:"container_id,omitempty" json:"container_id,omitempty"`
	BodyHash      string `yaml:"body_hash" json:"body_hash"`
	FileBlob      string `yaml:"file_blob,omitempty" json:"file_blob,omitempty"`
}
type Selector struct {
	QualifiedName string `yaml:"qualified_name" json:"qualified_name"`
	Kind          string `yaml:"kind" json:"kind"`
	File          string `yaml:"file" json:"file"`
}
type Binding struct {
	ID       string   `yaml:"id" json:"id"`
	SymbolID string   `yaml:"symbol_id" json:"symbol_id"`
	Selector Selector `yaml:"selector" json:"selector"`
	Baseline Baseline `yaml:"baseline" json:"baseline"`
}
type BindingFile struct {
	Version int       `yaml:"version" json:"version"`
	Anchors []Binding `yaml:"anchors" json:"anchors"`
	Path    string    `yaml:"-" json:"path"`
}
type Set struct {
	Policy      Policy     `json:"policy"`
	Specs       []Spec     `json:"specifications"`
	Bindings    []Binding  `json:"bindings"`
	Decisions   []Decision `json:"decisions"`
	Diagnostics []string   `json:"diagnostics,omitempty"`
	Digest      string     `json:"digest"`
}

// Diagnostic describes one invalid GPS authoring input. Validation collects
// independent document errors so an editor can fix a batch in one pass.
type Diagnostic struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func Init(repo string) error {
	root := filepath.Join(repo, Root)
	for _, part := range []string{"specs", "anchors"} {
		if err := os.MkdirAll(filepath.Join(root, part), 0o755); err != nil {
			return err
		}
	}
	path := filepath.Join(root, "intent.yaml")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", filepath.ToSlash(filepath.Join(Root, "intent.yaml")))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(path, []byte("version: 1\nspecs_root: specs\nanchors_root: anchors\n"), 0o644)
}

func Load(repo string) (Set, error) {
	set := Set{Policy: Policy{Version: 1, SpecsRoot: "specs", AnchorsRoot: "anchors"}}
	policyPath := filepath.Join(repo, Root, "intent.yaml")
	if _, err := os.Stat(policyPath); errors.Is(err, os.ErrNotExist) {
		set.Digest = digest(nil)
		return set, nil
	} else if err != nil {
		return set, err
	}
	if err := decodeFile(policyPath, &set.Policy); err != nil {
		return set, fmt.Errorf("read policy: %w", err)
	}
	if set.Policy.Version != 1 || !safeRelative(set.Policy.SpecsRoot) || !safeRelative(set.Policy.AnchorsRoot) {
		return set, errors.New("intent policy must use version 1 and relative roots")
	}
	if err := loadYAMLFiles(filepath.Join(repo, Root, set.Policy.SpecsRoot), func(path string) error {
		var spec Spec
		if err := decodeFile(path, &spec); err != nil {
			return err
		}
		spec.Path = rel(repo, path)
		if err := validateSpec(&spec); err != nil {
			return fmt.Errorf("%s: %w", spec.Path, err)
		}
		set.Specs = append(set.Specs, spec)
		return nil
	}); err != nil {
		return set, err
	}
	if err := loadYAMLFiles(filepath.Join(repo, Root, set.Policy.AnchorsRoot), func(path string) error {
		var bindings BindingFile
		if err := decodeFile(path, &bindings); err != nil {
			return err
		}
		if bindings.Version != 1 {
			return fmt.Errorf("%s: unsupported binding version", rel(repo, path))
		}
		for _, binding := range bindings.Anchors {
			if binding.ID == "" || binding.SymbolID == "" {
				return fmt.Errorf("%s: binding requires id and symbol_id", rel(repo, path))
			}
			set.Bindings = append(set.Bindings, binding)
		}
		return nil
	}); err != nil {
		return set, err
	}
	if err := loadYAMLFiles(filepath.Join(repo, Root, "decisions"), func(path string) error {
		var decision Decision
		if err := decodeFile(path, &decision); err != nil {
			return err
		}
		decision.Path = rel(repo, path)
		if decision.Version != 1 || decision.ID == "" || decision.Title == "" || decision.Decision == "" {
			return fmt.Errorf("%s: decision requires version 1, id, title, and decision", decision.Path)
		}
		set.Decisions = append(set.Decisions, decision)
		return nil
	}); err != nil {
		return set, err
	}
	if err := validateSet(&set); err != nil {
		return set, err
	}
	sort.Slice(set.Specs, func(i, j int) bool { return set.Specs[i].ID < set.Specs[j].ID })
	sort.Slice(set.Bindings, func(i, j int) bool { return set.Bindings[i].ID < set.Bindings[j].ID })
	sort.Slice(set.Decisions, func(i, j int) bool { return set.Decisions[i].ID < set.Decisions[j].ID })
	data, _ := yaml.Marshal(struct {
		Policy    Policy
		Specs     []Spec
		Bindings  []Binding
		Decisions []Decision
	}{set.Policy, set.Specs, set.Bindings, set.Decisions})
	set.Digest = digest(data)
	return set, nil
}

// Validate checks all independently readable intent documents. It deliberately
// does not replace Load: commands that consume intent continue to reject an
// incomplete set, while `spec validate` can return useful aggregate feedback.
func Validate(repo string) ([]Diagnostic, error) {
	policyPath := filepath.Join(repo, Root, "intent.yaml")
	if _, err := os.Stat(policyPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var policy Policy
	if err := decodeFile(policyPath, &policy); err != nil {
		return []Diagnostic{{Path: filepath.ToSlash(filepath.Join(Root, "intent.yaml")), Code: "E_POLICY_INVALID", Message: err.Error()}}, nil
	}
	if policy.Version != 1 || !safeRelative(policy.SpecsRoot) || !safeRelative(policy.AnchorsRoot) {
		return []Diagnostic{{Path: filepath.ToSlash(filepath.Join(Root, "intent.yaml")), Code: "E_POLICY_INVALID", Message: "intent policy must use version 1 and relative roots"}}, nil
	}
	var diagnostics []Diagnostic
	set := Set{Policy: policy}
	collect := func(root, code string, fn func(string) error) error {
		return loadYAMLFiles(root, func(path string) error {
			if err := fn(path); err != nil {
				diagnostics = append(diagnostics, Diagnostic{Path: rel(repo, path), Code: code, Message: err.Error()})
			}
			return nil
		})
	}
	if err := collect(filepath.Join(repo, Root, policy.SpecsRoot), "E_SPEC_INVALID", func(path string) error {
		var spec Spec
		if err := decodeFile(path, &spec); err != nil {
			return err
		}
		spec.Path = rel(repo, path)
		if err := validateSpec(&spec); err != nil {
			return err
		}
		set.Specs = append(set.Specs, spec)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := collect(filepath.Join(repo, Root, policy.AnchorsRoot), "E_BINDING_INVALID", func(path string) error {
		var bindings BindingFile
		if err := decodeFile(path, &bindings); err != nil {
			return err
		}
		if bindings.Version != 1 {
			return errors.New("unsupported binding version")
		}
		for _, binding := range bindings.Anchors {
			if binding.ID == "" || binding.SymbolID == "" {
				return errors.New("binding requires id and symbol_id")
			}
			set.Bindings = append(set.Bindings, binding)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := collect(filepath.Join(repo, Root, "decisions"), "E_DECISION_INVALID", func(path string) error {
		var decision Decision
		if err := decodeFile(path, &decision); err != nil {
			return err
		}
		decision.Path = rel(repo, path)
		if decision.Version != 1 || decision.ID == "" || decision.Title == "" || decision.Decision == "" {
			return errors.New("decision requires version 1, id, title, and decision")
		}
		set.Decisions = append(set.Decisions, decision)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := validateSet(&set); err != nil {
		diagnostics = append(diagnostics, Diagnostic{Path: filepath.ToSlash(Root), Code: "E_REFERENCE_INVALID", Message: err.Error()})
	}
	sort.Slice(diagnostics, func(i, j int) bool {
		if diagnostics[i].Path != diagnostics[j].Path {
			return diagnostics[i].Path < diagnostics[j].Path
		}
		if diagnostics[i].Code != diagnostics[j].Code {
			return diagnostics[i].Code < diagnostics[j].Code
		}
		return diagnostics[i].Message < diagnostics[j].Message
	})
	return diagnostics, nil
}

// LoadRevision reads only GPS inputs from one committed tree and reuses the
// regular strict loader, keeping code and intent selection in the same view.
func LoadRevision(ctx context.Context, repo, rev string) (Set, error) {
	paths, err := gitutil.ListFiles(ctx, repo, rev)
	if err != nil {
		return Set{}, err
	}
	tmp, err := os.MkdirTemp("", "entire-graph-intent-")
	if err != nil {
		return Set{}, err
	}
	defer os.RemoveAll(tmp)
	prefix := Root + "/"
	for _, path := range paths {
		if path != Root+"/intent.yaml" && !strings.HasPrefix(path, prefix) {
			continue
		}
		if !safeRelative(path) {
			return Set{}, fmt.Errorf("unsafe intent path %q", path)
		}
		content, ok, err := gitutil.ShowFile(ctx, repo, rev, path)
		if err != nil {
			return Set{}, err
		}
		if !ok {
			continue
		}
		target := filepath.Join(tmp, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return Set{}, err
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			return Set{}, err
		}
	}
	return Load(tmp)
}

func SaveBinding(repo string, binding Binding, update bool) error {
	set, err := Load(repo)
	if err != nil {
		return err
	}
	known := false
	for _, s := range set.Specs {
		for _, a := range s.Anchors {
			if a.ID == binding.ID {
				known = true
			}
		}
	}
	if !known {
		return fmt.Errorf("anchor %q is not declared by a specification", binding.ID)
	}
	path := filepath.Join(repo, Root, set.Policy.AnchorsRoot, "anchors.yaml")
	var file BindingFile
	if _, err := os.Stat(path); err == nil {
		if err := decodeFile(path, &file); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file.Version = 1
	for i := range file.Anchors {
		if file.Anchors[i].ID == binding.ID {
			if !update {
				return fmt.Errorf("binding %q already exists (pass --update to replace it)", binding.ID)
			}
			file.Anchors[i] = binding
			return writeYAML(path, file)
		}
	}
	file.Anchors = append(file.Anchors, binding)
	sort.Slice(file.Anchors, func(i, j int) bool { return file.Anchors[i].ID < file.Anchors[j].ID })
	return writeYAML(path, file)
}

func validateSpec(s *Spec) error {
	if s.Version != 1 || s.ID == "" || s.Title == "" || len(s.Requirements) == 0 {
		return errors.New("spec requires version 1, id, title, and requirements")
	}
	reqs, accepts := map[string]bool{}, map[string]bool{}
	for _, r := range s.Requirements {
		if r.ID == "" || r.Description == "" || reqs[r.ID] {
			return errors.New("requirements need unique ids and descriptions")
		}
		reqs[r.ID] = true
	}
	for _, a := range s.Acceptance {
		if a.ID == "" || a.Description == "" || !reqs[a.Requirement] || accepts[a.ID] {
			return errors.New("acceptance needs unique ids, descriptions, and known requirements")
		}
		accepts[a.ID] = true
	}
	for _, a := range s.Anchors {
		if a.ID == "" || !reqs[a.Requirement] {
			return errors.New("anchors need ids and known requirements")
		}
	}
	for _, relationship := range s.Relationships {
		if relationship.Target == "" || (relationship.Type != "parent_of" && relationship.Type != "depends_on" && relationship.Type != "related_to" && relationship.Type != "supersedes" && relationship.Type != "conflicts_with") {
			return errors.New("relationships need a supported type and target")
		}
	}
	for _, t := range s.Tests {
		if t.ID == "" || t.Selector.Name == "" || !accepts[t.Acceptance] {
			return errors.New("tests need ids, selectors, and known acceptance criteria")
		}
	}
	return nil
}
func validateSet(set *Set) error {
	ids := map[string]bool{}
	anchors := map[string]bool{}
	acceptances := map[string]bool{}
	tests := map[string]bool{}
	decisions := map[string]bool{}
	for _, s := range set.Specs {
		if ids[s.ID] {
			return fmt.Errorf("duplicate specification id %q", s.ID)
		}
		ids[s.ID] = true
		for _, acceptance := range s.Acceptance {
			if acceptances[acceptance.ID] {
				return fmt.Errorf("duplicate acceptance id %q", acceptance.ID)
			}
			acceptances[acceptance.ID] = true
		}
		for _, test := range s.Tests {
			if tests[test.ID] {
				return fmt.Errorf("duplicate test id %q", test.ID)
			}
			tests[test.ID] = true
		}
		for _, a := range s.Anchors {
			if anchors[a.ID] {
				return fmt.Errorf("duplicate anchor id %q", a.ID)
			}
			anchors[a.ID] = true
		}
	}
	for _, decision := range set.Decisions {
		if decisions[decision.ID] {
			return fmt.Errorf("duplicate decision id %q", decision.ID)
		}
		decisions[decision.ID] = true
		for _, spec := range decision.Affects {
			if !ids[spec] {
				return fmt.Errorf("decision %q affects unknown specification %q", decision.ID, spec)
			}
		}
		for _, anchor := range decision.Anchors {
			if !anchors[anchor] {
				return fmt.Errorf("decision %q references unknown anchor %q", decision.ID, anchor)
			}
		}
	}
	for _, spec := range set.Specs {
		for _, relationship := range spec.Relationships {
			if !ids[relationship.Target] {
				return fmt.Errorf("specification %q references unknown relationship target %q", spec.ID, relationship.Target)
			}
		}
	}
	seen := map[string]bool{}
	for _, b := range set.Bindings {
		if !anchors[b.ID] {
			return fmt.Errorf("binding %q is not declared", b.ID)
		}
		if seen[b.ID] {
			return fmt.Errorf("duplicate binding %q", b.ID)
		}
		seen[b.ID] = true
	}
	return nil
}
func decodeFile(path string, dest any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxDocumentBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxDocumentBytes {
		return fmt.Errorf("YAML document exceeds %d byte limit", maxDocumentBytes)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	if containsAlias(&document) {
		return errors.New("YAML aliases are not supported")
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(dest); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("YAML document must contain one document")
	}
	return nil
}

func containsAlias(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode {
		return true
	}
	for _, child := range node.Content {
		if containsAlias(child) {
			return true
		}
	}
	return false
}
func loadYAMLFiles(root string, fn func(string) error) error {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) && path == root {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".yaml" || ext == ".yml" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}
func writeYAML(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
func safeRelative(path string) bool {
	return path != "" && !filepath.IsAbs(path) && !strings.Contains(filepath.ToSlash(filepath.Clean(path)), "../") && filepath.Clean(path) != ".."
}
func rel(repo, path string) string {
	value, err := filepath.Rel(repo, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(value)
}
func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func Hash(value string) string { return digest([]byte(value)) }
