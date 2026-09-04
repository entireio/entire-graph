package sem

import "testing"

// writeGoPackageQualifierRepo lays out one interface requirement whose parameter
// is a package-qualified type, and three candidate implementations that spell
// that parameter three different ways: the same package, an ALIAS of the same
// package, and a DIFFERENT package that happens to export the same type name.
// Go accepts the first two and rejects the third.
func writeGoPackageQualifierRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/qual\n\ngo 1.21\n")
	writeFile(t, repo, "vendorish/redis/redis.go", `package redis

type Client struct {
	Addr string
}
`)
	writeFile(t, repo, "pkg/svc/svc.go", `package svc

import "net/http"

// Doer is satisfied only by a type whose Do takes net/http's Client.
type Doer interface {
	Do(c http.Client) error
	Ping() error
}
`)
	writeFile(t, repo, "pkg/httpimpl/httpimpl.go", `package httpimpl

import "net/http"

type Sender struct{}

func (s *Sender) Do(c http.Client) error { return nil }

func (s *Sender) Ping() error { return nil }
`)
	writeFile(t, repo, "pkg/aliasimpl/aliasimpl.go", `package aliasimpl

import nethttp "net/http"

type Alias struct{}

func (a *Alias) Do(c nethttp.Client) error { return nil }

func (a *Alias) Ping() error { return nil }
`)
	writeFile(t, repo, "pkg/redisimpl/redisimpl.go", `package redisimpl

import "example.com/qual/vendorish/redis"

type Cacher struct{}

func (c *Cacher) Do(cl redis.Client) error { return nil }

func (c *Cacher) Ping() error { return nil }
`)
	writeFile(t, repo, "pkg/consumer/consumer.go", `package consumer

import "example.com/qual/pkg/svc"

func run(d svc.Doer) error {
	return d.Ping()
}
`)
	return repo
}

// The interface-implementation hop must key a package-qualified parameter on the
// package the file's import block resolves the qualifier to, not on the bare type
// name. Both directions are asserted from one fixture: an alias of the SAME
// package still carries the hop (deleting that edge is the false decline the
// signature matcher exists to avoid), and a DIFFERENT package exporting the same
// type name does not (carrying that edge invents a call Go rejects).
func TestGoInterfaceHopKeysQualifiersOnTheResolvedImportPath(t *testing.T) {
	t.Parallel()
	repo := writeGoPackageQualifierRepo(t)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := callsFromTo(snapshot, "run", "Doer.Ping"); !ok {
		t.Fatalf("no CALLS run -> Doer.Ping; the fixture never reached the interface")
	}
	if _, ok := callsFromTo(snapshot, "run", "Sender.Ping"); !ok {
		t.Fatalf("no CALLS run -> Sender.Ping: http.Client vs http.Client is the same type")
	}
	if _, ok := callsFromTo(snapshot, "run", "Alias.Ping"); !ok {
		t.Fatalf("no CALLS run -> Alias.Ping: nethttp is an alias of net/http, so Alias implements Doer")
	}
	if _, ok := callsFromTo(snapshot, "run", "Cacher.Ping"); ok {
		t.Fatalf("CALLS run -> Cacher.Ping exists, but Cacher.Do takes vendorish/redis.Client, not net/http.Client, so Cacher does not implement Doer")
	}
}

// writeGoNestedModuleRepo puts a second go.mod under tools/, the shape the
// root-module-only ownership test mishandles: `example.com/tool/lib` is an
// in-repo package, but it is nested under the ROOT module's directory tree while
// belonging to a different module path.
func writeGoNestedModuleRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module example.com/root\n\ngo 1.21\n")
	writeFile(t, repo, "app/app.go", `package app

type Root struct{}

func (r *Root) Serve() error { return nil }
`)
	writeFile(t, repo, "tools/go.mod", "module example.com/tool\n\ngo 1.21\n")
	writeFile(t, repo, "tools/lib/lib.go", `package lib

type Runner struct {
	Name string
}

func (r *Runner) Run() error { return nil }
`)
	writeFile(t, repo, "tools/cmd/main.go", `package main

import "example.com/tool/lib"

func drive(r lib.Runner) error {
	return r.Run()
}
`)
	return repo
}

// A package-qualified receiver whose package belongs to a NESTED module is still
// repository-local source, so the method call must resolve. Deciding ownership
// against the root module alone declares `example.com/tool/lib` external and
// deletes the edge.
func TestGoQualifiedReceiverResolvesInsideANestedModule(t *testing.T) {
	t.Parallel()
	repo := writeGoNestedModuleRepo(t)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := callsFromTo(snapshot, "drive", "Runner.Run"); !ok {
		t.Fatalf("no CALLS drive -> Runner.Run: tools/ is its own module, and example.com/tool/lib is in-repo source")
	}
}
