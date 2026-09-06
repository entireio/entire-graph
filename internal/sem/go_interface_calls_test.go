package sem

import (
	"fmt"
	"strings"
	"testing"
)

// writeGoCommunicatorRepo reproduces terraform's provisioner shape: an interface
// in its own package, concrete implementations in sibling packages, and a
// consumer that receives the interface as a package-qualified parameter and calls
// through it.
func writeGoCommunicatorRepo(t *testing.T, implementations int) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/tflike\n\ngo 1.21\n")
	writeFile(t, repo, "internal/communicator/communicator.go", `package communicator

import "io"

// Communicator is implemented by every provisioner transport.
type Communicator interface {
	Connect(o string) error
	Disconnect() error
	ScriptPath() string
	Upload(path string, r io.Reader) error
}
`)
	for i := 0; i < implementations; i++ {
		name := fmt.Sprintf("t%d", i)
		writeFile(t, repo, "internal/communicator/"+name+"/"+name+".go", `package `+name+`

import "io"

type Communicator struct{}

func (c *Communicator) Connect(o string) error { return nil }

func (c *Communicator) Disconnect() error { return nil }

func (c *Communicator) ScriptPath() string { return "/tmp/script" }

func (c *Communicator) Upload(path string, r io.Reader) error { return nil }
`)
	}
	writeFile(t, repo, "internal/builtin/provisioners/remote-exec/resource_provisioner.go", `package remoteexec

import "example.com/tflike/internal/communicator"

func runScripts(comm communicator.Communicator) error {
	if err := comm.Connect("out"); err != nil {
		return err
	}
	defer comm.Disconnect()
	remotePath := comm.ScriptPath()
	_ = remotePath
	return nil
}
`)
	return repo
}

// callsFromTo reports whether a CALLS edge exists between the two qualified
// names, and returns the reason of the first match.
func callsFromTo(snapshot ProviderSnapshot, from, to string) (string, bool) {
	byID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		byID[s.ID] = s
	}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" {
			continue
		}
		source, sourceOK := byID[r.FromID]
		target, targetOK := byID[r.ToID]
		if !sourceOK || !targetOK {
			continue
		}
		if source.QualifiedName == from && target.QualifiedName == to {
			return r.Reason, true
		}
	}
	return "", false
}

// A Go interface method requirement must be a symbol with incoming CALLS. Before
// this, tree-sitter's interface body produced no member symbols at all, so
// terraform's communicator.Communicator had ZERO incoming edges and no traversal
// could cross the interface.
func TestGoInterfaceMethodHasIncomingCalls(t *testing.T) {
	t.Parallel()
	repo := writeGoCommunicatorRepo(t, 2)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	// The interface method exists as its own symbol, in the interface's file.
	var ifaceMethod *SymbolRecord
	for i, s := range snapshot.Symbols {
		if s.QualifiedName == "Communicator.Disconnect" && s.FilePath == "internal/communicator/communicator.go" {
			ifaceMethod = &snapshot.Symbols[i]
		}
	}
	if ifaceMethod == nil {
		t.Fatalf("no symbol for the interface method Communicator.Disconnect")
	}
	if ifaceMethod.Kind != "method" {
		t.Fatalf("interface requirement kind = %q, want method", ifaceMethod.Kind)
	}
	incoming := 0
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && r.ToID == ifaceMethod.ID {
			incoming++
		}
	}
	if incoming == 0 {
		t.Fatalf("interface method %s has no incoming CALLS", ifaceMethod.ID)
	}
	for _, method := range []string{"Connect", "Disconnect", "ScriptPath"} {
		if _, ok := callsFromTo(snapshot, "runScripts", "Communicator."+method); !ok {
			t.Fatalf("no CALLS runScripts -> Communicator.%s", method)
		}
	}
}

// The interface edge does not replace implementation binding: a call through the
// interface still reaches the concrete methods, so a caller-side traversal lands
// on real code.
func TestGoInterfaceCallCarriesToImplementations(t *testing.T) {
	t.Parallel()
	repo := writeGoCommunicatorRepo(t, 2)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]SymbolRecord{}
	for _, s := range snapshot.Symbols {
		byID[s.ID] = s
	}
	implFiles := map[string]bool{}
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" || r.Reason != "interface method call carried to the implementing method" {
			continue
		}
		target := byID[r.ToID]
		if target.Name == "Disconnect" {
			implFiles[target.FilePath] = true
		}
	}
	if len(implFiles) != 2 {
		t.Fatalf("Disconnect implementation hops = %v, want both t0 and t1", implFiles)
	}
	for file := range implFiles {
		if !strings.Contains(file, "/t0/") && !strings.Contains(file, "/t1/") {
			t.Fatalf("implementation hop landed outside the transports: %s", file)
		}
	}
}

// Past the fan-out cap the interface node stands alone: binding one polymorphic
// call to every implementation in the repository is noise, not resolution.
func TestGoInterfaceCallFanoutCapKeepsInterfaceEdgeOnly(t *testing.T) {
	t.Parallel()
	repo := writeGoCommunicatorRepo(t, goInterfaceImplementationFanoutCap+1)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" && r.Reason == "interface method call carried to the implementing method" {
			t.Fatalf("fan-out cap ignored: %s -> %s", r.FromID, r.ToID)
		}
	}
	// The interface hop itself must survive the cap.
	if _, ok := callsFromTo(snapshot, "runScripts", "Communicator.Disconnect"); !ok {
		t.Fatalf("interface edge dropped along with the capped implementations")
	}
}

// A one-method interface is satisfied by any type that happens to own a
// same-named method, so it must not fan out on name alone. This is the existing
// precision contract, retained now that the interface itself is a symbol.
func TestGoSingleMethodInterfaceDoesNotFanOutOnAmbiguousName(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/etcdlike\n\ngo 1.21\n")
	writeFile(t, repo, "server/server.go", `package server

import "example.com/etcdlike/auth"

type Server struct {
	authStore auth.AuthStore
}

func (s *Server) AuthStore() auth.AuthStore { return s.authStore }

func (s *Server) Close() error { return nil }

func (s *Server) shutdown() error {
	return s.AuthStore().Close()
}
`)
	writeFile(t, repo, "auth/store.go", `package auth

type AuthStore interface {
	Close() error
}

type authStore struct{}

func (as *authStore) Close() error { return nil }
`)
	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"authStore.Close", "Server.Close"} {
		if reason, ok := callsFromTo(snapshot, "Server.shutdown", name); ok {
			t.Fatalf("ambiguous one-method interface guessed an implementation: -> %s (%s)", name, reason)
		}
	}
}

// goInterfaceRequirementMethod discriminates a bare interface requirement from a
// real Go method declaration by the leading `func` keyword.
func TestGoInterfaceRequirementMethodDiscriminator(t *testing.T) {
	t.Parallel()
	requirement := SymbolRecord{Language: "Go", Kind: "method", Signature: "Disconnect() error"}
	concrete := SymbolRecord{Language: "Go", Kind: "method", Signature: "func (c *Communicator) Disconnect() error"}
	if !goInterfaceRequirementMethod(requirement) {
		t.Fatalf("bare interface requirement not recognised")
	}
	if goInterfaceRequirementMethod(concrete) {
		t.Fatalf("concrete method misread as an interface requirement")
	}
	other := SymbolRecord{Language: "TypeScript", Kind: "method", Signature: "disconnect(): void"}
	if goInterfaceRequirementMethod(other) {
		t.Fatalf("non-Go method classified as a Go interface requirement")
	}
}
