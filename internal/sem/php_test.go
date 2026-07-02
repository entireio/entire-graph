package sem

import (
	"strings"
	"testing"
)

// PHP call idioms the generic scanners miss (evidence: on composer/composer
// the focus method VersionSelector::findBestCandidate lost its static-call,
// property-receiver, and factory-typed-receiver edges): `Class::method(...)`
// scope-resolution calls, `$this->prop->method(...)` property receivers,
// `(new Klass(...))->method(...)` constructor chains, and `$v = $this->
// factory(); $v->method()` receivers typed by the factory's return type.
func TestPHPReceiverCallIdioms(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/Package/Version/VersionSelector.php", `<?php

namespace Composer\Package\Version;

use Composer\Filter\PlatformRequirementFilterFactory;
use Composer\Repository\RepositorySet;

class VersionSelector
{
    /** @var RepositorySet */
    private $repositorySet;

    public function __construct(RepositorySet $repositorySet)
    {
        $this->repositorySet = $repositorySet;
    }

    public function findBestCandidate(string $packageName, $platformRequirementFilter = null)
    {
        if (\Composer\Repository\PlatformRepository::isPlatformPackage($packageName)) {
            return false;
        }
        if (null === $platformRequirementFilter) {
            $platformRequirementFilter = PlatformRequirementFilterFactory::ignoreNothing();
        }
        $constraint = $this->getParser()->parseConstraints($packageName);
        return $this->repositorySet->findPackages(strtolower($packageName), $constraint);
    }

    private function getParser(): VersionParser
    {
        return new VersionParser();
    }
}

class VersionParser
{
    public function parseConstraints(string $constraints)
    {
        return $constraints;
    }
}
`)
	writeFile(t, repo, "src/Repository/RepositorySet.php", `<?php

namespace Composer\Repository;

class RepositorySet
{
    public function findPackages(string $name, $constraint = null): array
    {
        return [];
    }
}
`)
	writeFile(t, repo, "src/Repository/PlatformRepository.php", `<?php

namespace Composer\Repository;

class PlatformRepository
{
    public static function isPlatformPackage(string $name): bool
    {
        return false;
    }
}
`)
	writeFile(t, repo, "src/Filter/PlatformRequirementFilterFactory.php", `<?php

namespace Composer\Filter;

class PlatformRequirementFilterFactory
{
    public static function ignoreNothing()
    {
        return null;
    }
}
`)
	writeFile(t, repo, "src/Command/UpdateCommand.php", `<?php

namespace Composer\Command;

use Composer\Package\Version\VersionSelector;
use Composer\Repository\RepositorySet;

class UpdateCommand
{
    private function getPackagesInteractively(): array
    {
        $versionSelector = $this->createVersionSelector();
        $latest = $versionSelector->findBestCandidate("foo/bar");
        return [$latest];
    }

    private function createVersionSelector(): VersionSelector
    {
        return new VersionSelector(new RepositorySet());
    }
}
`)
	writeFile(t, repo, "src/Command/ArchiveCommand.php", `<?php

namespace Composer\Command;

use Composer\Package\Version\VersionSelector;
use Composer\Repository\RepositorySet;

class ArchiveCommand
{
    private function selectLocal(RepositorySet $repoSet)
    {
        $versionSelector = new VersionSelector($repoSet);
        return $versionSelector->findBestCandidate("a/b");
    }

    private function selectChained(RepositorySet $repoSet)
    {
        return (new VersionSelector($repoSet))->findBestCandidate("c/d");
    }

    private function selectNamespaced()
    {
        $selector = new \Composer\Package\Version\VersionSelector(new RepositorySet());
        return $selector->findBestCandidate("e/f");
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	calls := map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" {
			calls[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = r
		}
	}

	// use-imported ClassName::method(...) static call.
	if r, ok := calls["VersionSelector.findBestCandidate->PlatformRequirementFilterFactory.ignoreNothing"]; !ok || r.Resolution != "type_inferred" {
		t.Fatalf("use-imported static call not resolved: %#v", calls)
	}
	// Fully-qualified \Ns\Class::method(...) static call (terminal segment).
	if _, ok := calls["VersionSelector.findBestCandidate->PlatformRepository.isPlatformPackage"]; !ok {
		t.Fatalf("namespace-qualified static call not resolved: %#v", calls)
	}
	// $this->method() same-class call.
	if r, ok := calls["VersionSelector.findBestCandidate->VersionSelector.getParser"]; !ok || r.Confidence != 0.9 {
		t.Fatalf("$this-> same-class call not resolved (0.9): %#v", calls)
	}
	// $this->prop->method() where the property is typed by @var docblock and
	// constructor assignment.
	if r, ok := calls["VersionSelector.findBestCandidate->RepositorySet.findPackages"]; !ok || r.Reason != "method call resolved via typed property receiver" {
		t.Fatalf("typed property receiver call not resolved: %#v", calls)
	}
	// $v = $this->factory(); $v->method() typed by the factory's `: Type`
	// declared return type.
	if r, ok := calls["UpdateCommand.getPackagesInteractively->VersionSelector.findBestCandidate"]; !ok || r.Reason != "method call resolved via assigned returned receiver type" {
		t.Fatalf("factory-return receiver call not resolved: %#v", calls)
	}
	// $v = new Klass(...); $v->method() local constructor receiver.
	if r, ok := calls["ArchiveCommand.selectLocal->VersionSelector.findBestCandidate"]; !ok || r.Resolution != "type_inferred" {
		t.Fatalf("local constructor receiver call not resolved: %#v", calls)
	}
	// (new Klass(...))->method(...) constructor chain, with a nested call in
	// the local-var variant's arguments.
	if r, ok := calls["ArchiveCommand.selectChained->VersionSelector.findBestCandidate"]; !ok || r.Reason != "method call resolved via chained constructor type" {
		t.Fatalf("constructor-chain call not resolved: %#v", calls)
	}
	// $v = new \Ns\Klass(...) namespace-qualified constructor assignment types
	// the variable by the terminal class segment.
	if _, ok := calls["ArchiveCommand.selectNamespaced->VersionSelector.findBestCandidate"]; !ok {
		t.Fatalf("namespace-qualified constructor receiver call not resolved: %#v", calls)
	}
}

// self:: / static:: resolve within the enclosing class, parent:: resolves to
// the superclass even when the subclass overrides the method.
func TestPHPSelfStaticParentCalls(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/Filter.php", `<?php

namespace App;

class BaseFilter
{
    public function ignoreNothing()
    {
        return null;
    }
}

class Filter extends BaseFilter
{
    public function ignoreNothing()
    {
        return parent::ignoreNothing();
    }

    public static function fromBool(bool $value)
    {
        return self::fromList([$value]);
    }

    public static function fromList(array $list)
    {
        return static::build($list);
    }

    public static function build(array $list)
    {
        return $list;
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	calls := map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" {
			calls[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = r
		}
	}

	if r, ok := calls["Filter.fromBool->Filter.fromList"]; !ok || r.Confidence != 0.9 {
		t.Fatalf("self:: call not resolved (0.9): %#v", calls)
	}
	if _, ok := calls["Filter.fromList->Filter.build"]; !ok {
		t.Fatalf("static:: call not resolved: %#v", calls)
	}
	// parent:: must bind to the base-class method, not the override.
	if r, ok := calls["Filter.ignoreNothing->BaseFilter.ignoreNothing"]; !ok || !strings.Contains(r.Reason, "parent") {
		t.Fatalf("parent:: call not resolved to the superclass: %#v", calls)
	}
}

// Static-call and receiver scanning must not invent calls from PHP comments
// (`#`, `//`, `/* */`), string literals, or heredoc/nowdoc bodies, and
// promoted constructor properties must type property receivers.
func TestPHPCallScanPrecision(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "src/Mailer.php", `<?php

namespace App;

class Transport
{
    public function send(string $body)
    {
        return $body;
    }

    public function leak()
    {
        return null;
    }
}

class Mailer
{
    public function __construct(private Transport $transport)
    {
    }

    public function deliver(string $to)
    {
        # Transport::leak() in a hash comment
        // $x = (new Transport())->leak();
        /* Mailer::leak() in a block comment */
        $tpl = <<<HTML
            Transport::leak() and $this->transport->leak() in a heredoc
        HTML;
        $raw = 'Transport::leak()';
        return $this->transport->send($tpl . $raw . $to);
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	var sawSend bool
	for _, r := range snapshot.Relations {
		if r.Type != "CALLS" {
			continue
		}
		from, to := lastSegment(r.FromID), lastSegment(r.ToID)
		if to == "Transport.leak" || to == "Mailer.leak" {
			t.Fatalf("false call from comment/heredoc/string: %s -> %s (%s)", from, to, r.Reason)
		}
		if from == "Mailer.deliver" && to == "Transport.send" {
			sawSend = true
		}
	}
	if !sawSend {
		t.Fatalf("promoted constructor property receiver call not resolved: %#v", snapshot.Relations)
	}
}

// PHP `): Type` return declarations must populate RETURNS_TYPE references
// (splitSignatureTypes previously misread PHP signatures with the
// type-before-name convention and produced nothing).
func TestPHPSignatureReturnTypes(t *testing.T) {
	refs := signatureTypeReferences("PHP", "private function createVersionSelector(Composer $composer): VersionSelector")
	if len(refs["RETURNS_TYPE"]) != 1 || refs["RETURNS_TYPE"][0] != "VersionSelector" {
		t.Fatalf("RETURNS_TYPE = %#v", refs["RETURNS_TYPE"])
	}
	if len(refs["PARAM_TYPE"]) != 1 || refs["PARAM_TYPE"][0] != "Composer" {
		t.Fatalf("PARAM_TYPE = %#v", refs["PARAM_TYPE"])
	}
	refs = signatureTypeReferences("PHP", "public function findBestCandidate(string $packageName, ?IOInterface $io = null)")
	if len(refs["RETURNS_TYPE"]) != 0 {
		t.Fatalf("RETURNS_TYPE for no-return-type signature = %#v", refs["RETURNS_TYPE"])
	}
}

func TestPHPStaticCallsExtraction(t *testing.T) {
	calls := phpStaticCalls(`
        $a = PlatformRepository::isPlatformPackage($name);
        $b = \Composer\Semver\Constraint\Constraint::fromParts($x, $y);
        $c = self::create();
        $d = static::make();
        $e = parent::__construct();
        $f = BasePackage::STABILITIES[$stability]; // constant fetch, no call
        $g = Foo::class; // class constant, no call
        $h = $lower_case::run(); // not a class reference
    `)
	want := map[string]string{
		"PlatformRepository": "isPlatformPackage",
		"Constraint":         "fromParts",
		"self":               "create",
		"static":             "make",
		"parent":             "__construct",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %#v", calls)
	}
	for _, c := range calls {
		if want[c.Class] != c.Method {
			t.Fatalf("unexpected static call %#v", c)
		}
	}
	if calls[1].Detail != `\Composer\Semver\Constraint\Constraint::fromParts` {
		t.Fatalf("detail = %q", calls[1].Detail)
	}
}

func TestPHPPropertyTypes(t *testing.T) {
	types := phpPropertyTypes(`<?php
class VersionSelector
{
    /** @var RepositorySet */
    private $repositorySet;

    /** @var array<string, ConstraintInterface[]> */
    private $platformConstraints = [];

    private ?VersionParser $parser;

    protected static \Composer\IO\IOInterface $io;

    public function __construct(RepositorySet $repositorySet, private Config $config, ?PlatformRepository $platformRepo = null)
    {
        $this->repositorySet = $repositorySet;
        $this->platformRepo = $platformRepo;
    }
}
`)
	want := map[string]string{
		"repositorySet": "RepositorySet",
		"parser":        "VersionParser",
		"io":            "IOInterface",
		"config":        "Config",
		"platformRepo":  "PlatformRepository",
	}
	if len(types) != len(want) {
		t.Fatalf("types = %#v", types)
	}
	for name, typeName := range want {
		if types[name] != typeName {
			t.Fatalf("types[%q] = %q, want %q (all: %#v)", name, types[name], typeName, types)
		}
	}
}

// A property assigned two different types is dropped, and so is a local
// variable constructed from two different classes (conservative straight-line
// tracking).
func TestPHPConflictingBindingsDropped(t *testing.T) {
	types := phpPropertyTypes(`<?php
class C
{
    private Foo $thing;

    public function __construct(Bar $thing)
    {
        $this->thing = $thing;
    }
}
`)
	if _, ok := types["thing"]; ok {
		t.Fatalf("conflicting property type not dropped: %#v", types)
	}
	locals := phpLocalVarTypes(`
        $v = new Foo();
        $v = new Bar();
        $w = new Baz();
    `)
	if _, ok := locals["v"]; ok {
		t.Fatalf("conflicting local type not dropped: %#v", locals)
	}
	if locals["w"] != "Baz" {
		t.Fatalf("locals = %#v", locals)
	}
}

func TestPHPChainedConstructorCalls(t *testing.T) {
	calls := phpChainedConstructorCalls(`
        $a = (new VersionSelector(new RepositorySet(), $platformRepo))->findBestCandidate($name);
        $b = new ArrayLoader($this->getParser())->load($config); // PHP 8.4 parens-less chain
        $c = new Plain($x); // no chain
    `)
	if len(calls) != 2 {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].TypeName != "VersionSelector" || calls[0].Method != "findBestCandidate" {
		t.Fatalf("calls[0] = %#v", calls[0])
	}
	if calls[1].TypeName != "ArrayLoader" || calls[1].Method != "load" {
		t.Fatalf("calls[1] = %#v", calls[1])
	}
}

func TestPHPStripCodeText(t *testing.T) {
	stripped := stripPHPCodeText(`<?php
$a = Real::call();
# Hash::comment();
$sql = <<<SQL
    Heredoc::body();
SQL;
$now = <<<'TXT'
    Nowdoc::body();
TXT;
$b = Other::call();
`)
	for _, gone := range []string{"Hash::", "Heredoc::", "Nowdoc::"} {
		if strings.Contains(stripped, gone) {
			t.Fatalf("%q not stripped:\n%s", gone, stripped)
		}
	}
	for _, kept := range []string{"Real::call", "Other::call"} {
		if !strings.Contains(stripped, kept) {
			t.Fatalf("%q wrongly stripped:\n%s", kept, stripped)
		}
	}
}
