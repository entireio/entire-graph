package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const AdapterVersion = "gopls-capsule-v1"
const PinnedServerVersion = "v0.20.0"

type Config struct {
	// OperationID binds the caller's captured view, policy and source scope.
	// Direct adapter tests may omit it and use the capsule-only fallback.
	OperationID string
	// Trusted installed paths, never obtained from files in the analyzed repo.
	ServerPath     string
	ServerSHA256   string
	ToolchainRoot  string
	BubblewrapPath string
	Tags           []string
}
type Diagnostic struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
	URI    string `json:"uri,omitempty"`
}
type Query struct {
	IncludeCandidates bool   `json:"include_candidates,omitempty"`
	Path              string `json:"path"`
	Offset            int    `json:"offset"`
	Implementation    bool   `json:"implementation,omitempty"`
}
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}
type Answer struct {
	Query   Query      `json:"query"`
	Kind    string     `json:"kind"`
	Targets []Location `json:"targets"`
}
type Report struct {
	Packages      []string     `json:"packages,omitempty"`
	Configuration []string     `json:"configuration,omitempty"`
	ToolchainID   string       `json:"toolchain_id,omitempty"`
	Status        string       `json:"status"`
	Backend       string       `json:"backend"`
	ContextID     string       `json:"context_id,omitempty"`
	Queries       int          `json:"queries"`
	Answers       []Answer     `json:"answers,omitempty"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
}

// Analyze runs one fresh server in an immutable, operation-owned capsule. The
// caller supplies already captured source, never a live repository directory.
func Analyze(ctx context.Context, config Config, files map[string]string, queries []Query) Report {
	report := Report{Status: "unavailable", Backend: "gopls/" + PinnedServerVersion}
	fail := func(code string, err error) Report {
		report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: code, Detail: err.Error()})
		if len(report.Answers) > 0 || report.Status == "complete" {
			report.Status = "partial"
		}
		return report
	}
	if err := validateConfig(config); err != nil {
		return fail("compiler_unavailable", err)
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	capsule, inputs, err := createCapsule(files)
	if err != nil {
		return fail("compiler_capsule_unavailable", err)
	}
	defer os.RemoveAll(capsule)
	modules, err := discoverCapsule(ctx, config, capsule)
	if err != nil {
		return fail("compiler_dependency_unavailable", err)
	}
	report.Packages = modules
	report.Configuration = append([]string{"CGO_ENABLED=0", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOOS=" + runtime.GOOS, "GOARCH=" + runtime.GOARCH}, config.Tags...)
	report.ToolchainID = toolchainIdentity(config.ToolchainRoot)
	var operationParts strings.Builder
	for _, input := range inputs {
		fmt.Fprintf(&operationParts, "%d:%s%d:%s", len(input.Path), input.Path, len(input.Digest), input.Digest)
	}
	operationID := config.OperationID
	if operationID == "" {
		operationID = ContentDigest(operationParts.String())
	}
	report.ContextID, err = (BuildContext{OperationID: operationID, Inputs: inputs, Configuration: report.Configuration, Packages: modules, AdapterVersion: AdapterVersion, ServerVersion: config.ServerSHA256, ToolchainVersion: report.ToolchainID}).ID()
	if err != nil {
		return fail("compiler_context_unavailable", err)
	}
	process, err := launchIsolated(ctx, config, capsule)
	if err != nil {
		return fail("compiler_unavailable", err)
	}
	defer process.close()
	client := newRPCClient(ctx, process.stdout, process.stdin)
	initialization := map[string]any{"telemetryPrompt": false, "checkUpdates": "off", "staticcheck": false}
	if len(config.Tags) > 0 {
		initialization["buildFlags"] = []string{"-tags=" + strings.Join(config.Tags, ",")}
	}
	client.configuration = initialization
	result, err := client.request("initialize", map[string]any{"processId": nil, "rootUri": "file:///workspace", "capabilities": map[string]any{"general": map[string]any{"positionEncodings": []string{"utf-16"}}}, "initializationOptions": initialization})
	if err != nil {
		return fail("compiler_initialize_failed", err)
	}
	var initialized struct {
		Capabilities struct {
			PositionEncoding       string          `json:"positionEncoding"`
			DefinitionProvider     json.RawMessage `json:"definitionProvider"`
			ImplementationProvider json.RawMessage `json:"implementationProvider"`
		} `json:"capabilities"`
		ServerInfo struct {
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if json.Unmarshal(result, &initialized) != nil {
		return fail("compiler_initialize_failed", errors.New("invalid initialize response"))
	}
	if encoding := initialized.Capabilities.PositionEncoding; encoding != "" && encoding != "utf-16" {
		return fail("compiler_encoding_unavailable", fmt.Errorf("unsupported position encoding %q", encoding))
	}
	if !capabilityEnabled(initialized.Capabilities.DefinitionProvider) {
		return fail("compiler_definition_unavailable", errors.New("server does not advertise definition"))
	}
	// gopls v0.20 reports build info as a JSON string, including Version.
	var version struct {
		Version string `json:"Version"`
	}
	if json.Unmarshal([]byte(initialized.ServerInfo.Version), &version) != nil || version.Version != PinnedServerVersion {
		return fail("compiler_version_mismatch", errors.New("server is not the pinned gopls version"))
	}
	if err := client.notify("initialized", struct{}{}); err != nil {
		return fail("compiler_initialize_failed", err)
	}
	ordered := append([]Query(nil), queries...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Path != ordered[j].Path {
			return ordered[i].Path < ordered[j].Path
		}
		if ordered[i].Offset != ordered[j].Offset {
			return ordered[i].Offset < ordered[j].Offset
		}
		return !ordered[i].Implementation && ordered[j].Implementation
	})
	report.Status = "complete"
	if len(ordered) > MaxQueries {
		ordered = ordered[:MaxQueries]
		report.Status = "partial"
		report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: "compiler_query_limit"})
	}
	opened := map[string]bool{}
	responseBytes := 0
	for index := 0; index < len(ordered); index++ {
		query := ordered[index]
		if report.Queries >= MaxQueries {
			report.Status = "partial"
			report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: "compiler_query_limit"})
			break
		}
		content, ok := files[query.Path]
		if !ok {
			return fail("compiler_source_unavailable", errors.New("query path is not captured"))
		}
		position, err := PositionAt(content, query.Offset)
		if err != nil {
			return fail("compiler_position_invalid", err)
		}
		uri := (&url.URL{Scheme: "file", Path: "/workspace/" + query.Path}).String()
		if !opened[query.Path] {
			if err := client.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "go", "version": 1, "text": content}}); err != nil {
				return fail("compiler_document_unavailable", err)
			}
			opened[query.Path] = true
		}
		method := "textDocument/definition"
		if query.Implementation {
			method = "textDocument/implementation"
			if !capabilityEnabled(initialized.Capabilities.ImplementationProvider) {
				report.Status = "partial"
				report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: "compiler_implementation_unavailable"})
				continue
			}
		}
		result, err := client.request(method, map[string]any{"textDocument": map[string]any{"uri": uri}, "position": position})
		report.Queries++
		if err != nil {
			report.Status = "partial"
			return fail("compiler_request_failed", err)
		}
		responseBytes += len(result)
		if responseBytes > MaxMessageBytes {
			report.Status = "partial"
			return fail("compiler_response_budget", errors.New("aggregate compiler response limit"))
		}
		locations, err := decodeLocations(result)
		if err != nil {
			return fail("compiler_target_invalid", err)
		}
		if len(locations) == 0 && !query.Implementation {
			report.Status = "partial"
			report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: "compiler_no_target", Detail: query.Path})
		}
		for _, location := range locations {
			if _, _, _, err := MapLocation(files, location); err != nil {
				report.Status = "partial"
				report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: "compiler_target_outside_capture", Detail: err.Error()})
			}
		}
		if query.IncludeCandidates && !query.Implementation && len(locations) == 1 && interfaceLocation(files, locations[0]) {
			candidate := query
			candidate.Implementation = true
			candidate.IncludeCandidates = false
			ordered = append(ordered, Query{})
			copy(ordered[index+2:], ordered[index+1:])
			ordered[index+1] = candidate
		}
		report.Answers = append(report.Answers, Answer{Query: query, Kind: method, Targets: locations})
	}
	if _, err := client.request("shutdown", nil); err != nil {
		report.Status = "partial"
		return fail("compiler_shutdown_failed", err)
	}
	if err := client.notify("exit", nil); err != nil {
		report.Status = "partial"
		return fail("compiler_shutdown_failed", err)
	}
	report.Diagnostics = append(report.Diagnostics, client.diagnostics...)
	if len(report.Diagnostics) > 0 {
		report.Status = "partial"
	}
	return report
}
func capabilityEnabled(value json.RawMessage) bool {
	return len(value) > 0 && string(value) != "false" && string(value) != "null"
}
func decodeLocations(value json.RawMessage) ([]Location, error) {
	if string(value) == "null" {
		return nil, nil
	}
	var locations []Location
	if json.Unmarshal(value, &locations) != nil {
		var location Location
		if err := json.Unmarshal(value, &location); err != nil {
			return nil, err
		}
		locations = []Location{location}
	}
	for _, location := range locations {
		if location.URI == "" {
			return nil, errors.New("location links or empty locations are unsupported")
		}
	}
	sort.Slice(locations, func(i, j int) bool {
		a, b := locations[i], locations[j]
		if a.URI != b.URI {
			return a.URI < b.URI
		}
		if a.Range.Start.Line != b.Range.Start.Line {
			return a.Range.Start.Line < b.Range.Start.Line
		}
		return a.Range.Start.Character < b.Range.Start.Character
	})
	return locations, nil
}
func MapLocation(files map[string]string, location Location) (string, int, int, error) {
	uri, err := url.Parse(location.URI)
	if err != nil || uri.Scheme != "file" || uri.Host != "" || uri.RawQuery != "" || uri.Fragment != "" || !strings.HasPrefix(uri.Path, "/workspace/") {
		return "", 0, 0, errors.New("target is not a captured file URI")
	}
	name := strings.TrimPrefix(uri.Path, "/workspace/")
	if path.Clean(name) != name || strings.HasPrefix(name, "../") {
		return "", 0, 0, errors.New("noncanonical target path")
	}
	source, ok := files[name]
	if !ok {
		return "", 0, 0, errors.New("target file absent from capsule")
	}
	start, err := OffsetAt(source, location.Range.Start)
	if err != nil {
		return "", 0, 0, err
	}
	end, err := OffsetAt(source, location.Range.End)
	if err != nil || end < start {
		return "", 0, 0, errors.New("invalid target range")
	}
	return name, start, end, nil
}
func createCapsule(files map[string]string) (string, []Input, error) {
	directory, err := os.MkdirTemp("", "entire-graph-compiler-")
	if err != nil {
		return "", nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		os.RemoveAll(directory)
		return "", nil, err
	}
	defer root.Close()
	cleanup := func(err error) (string, []Input, error) { os.RemoveAll(directory); return "", nil, err }
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var total int64
	var inputs []Input
	for _, name := range names {
		if name == "." || name == "" || !filepath.IsLocal(name) || strings.Contains(name, "\\") || path.Clean(name) != name || strings.HasPrefix(name, ".git/") || name == ".git" {
			return cleanup(errors.New("unsafe compiler capsule path"))
		}
		total += int64(len(files[name]))
		if total > 256<<20 || len(names) > 10000 {
			return cleanup(errors.New("compiler capsule input limit"))
		}
		if err := root.MkdirAll(filepath.FromSlash(path.Dir(name)), 0700); err != nil {
			return cleanup(err)
		}
		file, err := root.OpenFile(filepath.FromSlash(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
		if err != nil {
			return cleanup(err)
		}
		_, writeErr := file.WriteString(files[name])
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return cleanup(err)
		}
		inputs = append(inputs, Input{Path: name, Digest: ContentDigest(files[name]), Present: true})
	}
	for _, manifest := range []string{"go.mod", "go.sum", "go.work", "go.work.sum"} {
		if _, ok := files[manifest]; !ok {
			inputs = append(inputs, Input{Path: manifest, Present: false})
		}
	}
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Path < inputs[j].Path })
	return directory, inputs, nil
}
func toolchainIdentity(root string) string {
	bytes, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		return ""
	}
	binary, err := os.ReadFile(filepath.Join(root, "bin", "go"))
	if err != nil {
		return ""
	}
	return ContentDigest(string(bytes) + "\x00" + ContentDigest(string(binary)))
}

func interfaceLocation(files map[string]string, location Location) bool {
	name, start, end, err := MapLocation(files, location)
	if err != nil {
		return false
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, files[name], 0)
	if err != nil {
		return false
	}
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		if iface, ok := node.(*ast.InterfaceType); ok {
			for _, field := range iface.Methods.List {
				for _, ident := range field.Names {
					if fset.Position(ident.Pos()).Offset == start && fset.Position(ident.End()).Offset == end {
						found = true
					}
				}
			}
		}
		return true
	})
	return found
}
