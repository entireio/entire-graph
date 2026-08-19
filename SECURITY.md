# Security Policy

Entire Graph is a local, no-egress code-intelligence plugin. It parses a Git
repository on your machine and answers queries about it. That shape determines
the whole threat model below: **the repository is the untrusted input.**

## Relationship to the organization policy

This policy governs `entireio/entire-graph` and **overrides** the
organization-wide policy at
[`entireio/.github`](https://github.com/entireio/.github/blob/main/SECURITY.md)
wherever the two differ about this repository's local attack surface.

The override is deliberate, because the org policy's routing rule inverts
itself when applied here. That policy issues advisories only for a
vulnerability "exploited by a remote or non-local actor", and sends
local-execution problems — resource exhaustion that "requires local access to
trigger", and "issues that cannot be exploited without direct access to the
user's machine" — to public GitHub Issues as ordinary bug reports. It also
lists "denial of service attacks" as out of scope outright.

Entire Graph has no server and no remote surface. It is a binary you run on
your own machine, so *every* vulnerability it can have is a local-execution
vulnerability, and read literally the org rule would route all of them to a
public tracker. The framing is what is wrong, not the rule: the attacker here
*is* remote in the way that matters — they wrote the repository you cloned —
and their payload merely detonates locally, on your machine, at the moment you
run an ordinary query. So on this repository:

- Report privately, per the section below. The local-execution carve-out does
  not apply here.
- Hostile-content denial of service **is** in scope, notwithstanding the org
  policy's blanket exclusion. A repository that hangs or exhausts the machine
  of anyone who merely clones and indexes it is an attack, not a slow test.

Everything the org policy covers that is not this attack surface — the `entire`
CLI proper, Entire's hosted services, org-wide contact and confidentiality
commitments — applies unchanged.

## Reporting a vulnerability

**Do not open a public issue for a suspected vulnerability**, and do not open
an issue asking who to contact. GitHub's own guidance suggests that second one
for repositories with no private reporting channel; it is unnecessary here,
because the contact is right below.

**Email [security@entire.io](mailto:security@entire.io).** This is the private
channel that works today, and it is the one to use.

GitHub's **Report a vulnerability** button is not available on this repository.
That button is gated on the repository's
[private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/working-with-repository-security-advisories/configuring-private-vulnerability-reporting-for-a-repository)
setting, which is currently disabled — a repository setting, which publishing
this file does not change. If a maintainer enables it later it becomes a second
private channel of equal standing, and this section should be updated to say
so. Until then, email is the channel.

Please include: what the bug is, what an attacker gains, a minimal reproduction
(the repository shape and the exact command matter far more than prose here —
see below), the affected version from `entire graph version`, and a suggested
fix if you have one.

## Supported versions

**The most recent tagged release is supported, along with `main`.** Earlier
tags are not. Fixes ship in the next release cut from `main`; there are no
backports.

If you are on an older tag, upgrade and re-check before reporting, or tell us
which tag you tested so we can work out whether the bug survives. Report what
`entire graph version` prints.

## Threat model: what is in scope

Entire Graph is routinely pointed at repositories the user did not write and
has not read: a fresh clone, a contributor's branch, a dependency checkout, a
repository an agent was told to investigate. Merely running `entire graph` over
a repository must not compromise the machine.

So **repository-controlled content is the primary attacker channel**, and that
means all of it:

- **File and directory names** — including `..` segments, absolute paths,
  unicode and encoding tricks, and names that collide after normalization.
- **File contents** — anything the tree-sitter parsers and the ranking code
  consume.
- **Symlinks** — inside the tree, and pointing anywhere outside it.
- **`.gitignore`, nested `.gitignore`, `.git/info/exclude`, `.graphignore`** —
  and any path they can cause the tool to resolve.
- **Git configuration in the repository, including `core.excludesFile`** —
  repository-controlled config that names paths the tool then reads.

Reports we specifically want, phrased as what you would demonstrate:

- **Writing outside the intended destination.** Any repository content that
  causes a write outside the cache directory or outside the explicit path the
  user passed — via traversal, symlink following, or an absolute path. The
  write surfaces are the cache, `index --report <path>`,
  `verify --record-baseline <path>`, and `init-agents`. Queries are documented
  as never modifying repository source files; a case where one does is a bug
  under this policy.
- **Reading outside the repository.** Repository-controlled ignore, include,
  or config paths that pull file contents from elsewhere on disk into output.
- **Execution the user did not ask for.** Anything in a repository that gets
  executed by an ordinary query. Note the documented boundary: `search`
  *suggests* a `VERIFY:` command derived from repository contents and does not
  run it. A path where that text, or any other repository content, actually
  executes is in scope.
- **Any network call.** The built-in analyzer is no-egress by design: no
  remote fetches, no model or hosted API calls, no grammar downloads, no
  telemetry. **An observed network call from analysis or query commands is
  itself a reportable security bug**, no further impact needed. The two
  documented networked operations are installation
  (`entire plugin install graph`) and the benchmark harness's clone phase;
  neither is a finding.
- **Denial of service from a repository the user merely cloned** — a crash,
  hang, unbounded memory, or pathological blowup triggered by hostile content
  rather than by size.

See [docs/trust-and-security.md](docs/trust-and-security.md) for the precise
read, write, execute, and network surfaces these map onto.

## Out of scope

- **Hostile flags the victim types themselves.** `verify` executes the test
  command you give it, and `--setup` executes the setup command you give it,
  both with your privileges — that is the documented contract. Likewise
  pointing `--cache-dir`, `--report`, or `--record-baseline` at a sensitive
  path. If the attack requires the user to run a command an attacker wrote,
  report it upstream to whatever handed them that command.
- **Anything requiring pre-existing write access to the machine.** If the
  attacker can already write the cache directory, the config, the `PATH`, or
  the binary, they did not need this tool.
- **Resource exhaustion on a repository the user authored.** A large monorepo
  indexing slowly, or a generated file the parser is slow on, is a performance
  bug — please file it as a regular issue. Hostile-content DoS (above) is
  different and is in scope.
- **Third-party dependency advisories** with no demonstrated impact here.
  Report those upstream first.
- Static analysis being incomplete. Missed or unresolved edges from dynamic
  dispatch, reflection, or generated code are documented limits, not
  vulnerabilities.

## What to expect

These are the organization policy's published commitments, and they apply here
unchanged:

- **Acknowledgment within 48 hours** of your report.
- **Progress updates** while we investigate.
- **A 90-day resolution target** for confirmed critical issues — usually much
  sooner for something this small and local.
- **Confidentiality.** Reports are kept confidential; your information is not
  shared with third parties without your consent, except as required by law.
- **Credit for your contribution.** We make every effort to acknowledge
  reporters.

This repository adds no further commitment on top of those — no bounty, no
tighter SLA, and no default embargo or coordinated-disclosure agreement. If you
need a publication date or a disclosure timeline agreed, say so in your first
email and we will settle it with you explicitly, rather than leave you relying
on a default that does not exist.

If you are unsure whether what you found counts, report it privately anyway.
Deciding scope is our job, not yours.
