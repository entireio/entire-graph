# Windows CI benchmark harness

This directory provides a common measurement harness for the Windows CI parity
investigation. It records the unmodified Go suite, host details, resource
samples, heavy-package compile/link measurements, and parsed package/test
timings. Use a new result directory for every repetition and retain failed and
slower runs as well as successful ones.

## Benchmark contract

Unless an experiment explicitly varies infrastructure or scheduling, use this
contract:

```text
Repository:   entireio/entire-graph
Commit:       ee6468a6a49d9b2a1a828bd276792f415f392185
Go:           1.26.7 (go env GOVERSION reports go1.26.7)
Target:       windows/amd64
CGO_ENABLED:  1
Measured run: go test -json -timeout 30m ./...
```

The product checkout must be detached at the pinned commit. The harness may be
copied alongside that checkout or into it as untracked files; do not benchmark
a different product tree. Do not add skips, `-short`, smaller fixtures, changed
timeouts, mocks, or test-selection filters to a baseline. Keep Defender enabled
for the primary benchmark.

## Prerequisites

On the Windows benchmark host:

- 64-bit Windows and PowerShell 7 (`pwsh`), not Windows PowerShell 5.1;
- Go 1.26.7, Git for Windows, and MinGW-w64/GCC on `PATH`;
- Python 3 when parsing results on the host;
- the pinned repository checkout and all three `.ps1` files in this directory.

On a machine controlling Azure:

- Bash, Azure CLI authenticated to the intended subscription, and permission to
  create/delete resource groups, networks, disks, VMs, and Run Command actions;
- `openssl` unless `AZURE_VM_ADMIN_PASSWORD` is supplied;
- Python 3 for local parsing.

Use `--subscription ID` on every Azure helper when the active subscription is
not unambiguous. Never put credentials, SAS URLs, tokens, or passwords in a Run
Command script, arguments, console output, or retained artifacts.

## Windows-first validation

The PowerShell scripts use Windows CIM/performance classes and must be parsed
and exercised on a disposable Windows host before measurements are trusted.
From PowerShell 7, first verify the contract:

```powershell
pwsh --version
go version
git --version
gcc --version
go env GOOS GOARCH CGO_ENABLED GOVERSION
git -C C:\src\entire-graph rev-parse HEAD
```

Then parse all scripts without executing them:

```powershell
$harness = 'C:\ci-bench\tools\ci-bench'
Get-ChildItem -LiteralPath $harness -Filter '*.ps1' | ForEach-Object {
    $tokens = $null
    $parseErrors = $null
    [System.Management.Automation.Language.Parser]::ParseFile(
        $_.FullName,
        [ref] $tokens,
        [ref] $parseErrors
    ) | Out-Null
    if ($parseErrors.Count -ne 0) {
        throw "PowerShell parse failure in $($_.FullName): $($parseErrors -join '; ')"
    }
}
```

Run the bounded environment and monitor examples below before the full suite.
Inspect `environment.json.errors` and the `errors` field of every resource
sample; a file being present does not by itself prove all Windows counters were
available.

## Local Windows operation

Use result paths outside the product checkout so result files do not make the
checkout dirty. The examples assume the harness is in `C:\ci-bench` and the
product checkout is in `C:\src\entire-graph`.

### Collect the environment only

```powershell
$harness = 'C:\ci-bench\tools\ci-bench'
$repo = 'C:\src\entire-graph'
$result = 'C:\ci-bench-results\a1\environment-smoke'
$now = [DateTimeOffset]::UtcNow.ToString('o')

pwsh -NoProfile -File "$harness\collect-environment.ps1" `
    -OutputDirectory $result `
    -RepositoryPath $repo `
    -RunId 'a1-environment-smoke' `
    -RunLabel 'environment-smoke' `
    -CommandLine 'go test -json -timeout 30m ./...' `
    -StartTimeUtc $now `
    -EndTimeUtc $now `
    -ExitCode 0
```

This writes `environment.json` and raw `systeminfo.txt`. Optional facilities
such as Azure instance metadata or Defender cmdlets are captured as partial
errors rather than discarding the rest of the environment record.

### Sample resources independently

This bounded smoke test writes five one-second samples and exits:

```powershell
$harness = 'C:\ci-bench\tools\ci-bench'
$result = 'C:\ci-bench-results\a1\monitor-smoke'
New-Item -ItemType Directory -Path $result -Force | Out-Null

pwsh -NoProfile -File "$harness\monitor-resources.ps1" `
    -OutputPath "$result\resource-samples.jsonl" `
    -IntervalSeconds 1 `
    -MaxSamples 5
```

`run-go-suite.ps1` starts and stops this monitor automatically. For standalone
long-running use, pass `-StopFile PATH` and create that exact file to stop it;
`-MonitoredProcessId PID` also stops after the selected process exits.

### Run the complete baseline

Use a fresh output directory for each repetition:

```powershell
$harness = 'C:\ci-bench\tools\ci-bench'
$repo = 'C:\src\entire-graph'
$result = 'C:\ci-bench-results\a1\baseline-01'

pwsh -NoProfile -File "$harness\run-go-suite.ps1" `
    -OutputDirectory $result `
    -RepositoryPath $repo `
    -RunId 'a1-baseline-01' `
    -RunLabel 'warm-build-cache-baseline' `
    -ExpectedRepositorySha 'ee6468a6a49d9b2a1a828bd276792f415f392185' `
    -ExpectedGoVersion 'go1.26.7'
$suiteWrapperExitCode = $LASTEXITCODE
Write-Host "suite wrapper exit code: $suiteWrapperExitCode"
```

For the baseline, do not pass `-Packages`, `-AdditionalGoTestArguments`,
`-CgoEnabled`, `-SkipCompileDecomposition`, or `-SkipResourceMonitor`; their
defaults produce the contract above. The runner forces `GOOS=windows`,
`GOARCH=amd64`, and, by default, `CGO_ENABLED=1`.

### Parse Go test JSON

The parser is dependency-free and may run on Windows or after results have been
exported:

```powershell
python "$harness\parse-go-test-json.py" `
    --input "$result\go-test.jsonl" `
    --output "$result\go-test-summary.json" `
    --summary-output "$result\go-test-summary.txt" `
    --top 30
```

The parser tolerates malformed/truncated lines and reports them under
`diagnostics`. Its exit code indicates parser I/O/usage success, not Go test
success. Accept a run only after checking both `run-metadata.json` and that the
parsed `suite.status` is `pass`, `suite.complete` is `true`, and relevant
diagnostics have been resolved.

## Cache and timing semantics

Every suite run executes `go clean -testcache` immediately before the measured
`go test`. The Go build cache and module cache are preserved by default. This is
the normal warm-build-cache benchmark.

`-ColdBuildCache` additionally runs `go clean -cache`; label such a run
`cold-build-cache`. It does not clear the module cache. Do not manually clear a
cache without recording that variation.

After the measured suite, the runner compiles these native test binaries with
`-vet=off -c` and records independent wall times and sizes:

```text
./internal/sem      -> sem.test.exe
./internal/cli      -> cli.test.exe
./internal/gitutil  -> gitutil.test.exe
```

Those compile measurements intentionally run after the suite and therefore use
the build cache warmed by it. They are warm compile/link decomposition data, not
a clean-build measurement.

## Exit codes and durable failure metadata

The measured Go stdout is piped through `Tee-Object` into `go-test.jsonl`; stderr
is retained separately. The runner captures `$LASTEXITCODE` immediately after
the native pipeline. `run-metadata.json.goTestExitCode` is therefore the real
measured Go exit code even when later collection succeeds or fails.

The wrapper exits with that code when the suite fails. If the suite passes but a
subsequent heavy-package compilation fails, it exits with that compilation
code; setup failures exit nonzero. `run-metadata.json.exitCode` is the final
wrapper code. The runner writes initial metadata before validation and updates
it in `finally`, so inspect partial results even after a nonzero exit.

## Result layout

Each suite result directory contains:

| Path | Contents |
|---|---|
| `run-metadata.json` | Schema version, contract, command, SHA, phases/timings, real Go and wrapper exit codes, errors, and output paths. |
| `go-test.jsonl` / `go-test.stderr.log` | Raw measured Go stdout events and stderr. |
| `run-command.txt` | Exact measured command line. |
| `resource-samples.jsonl` | Versioned start record, CPU/memory/per-disk samples, and end record. |
| `environment.json` / `systeminfo.txt` | Windows/image/Azure SKU, CPU/RAM/storage, tools, `go env`, Defender, repository, and run metadata. |
| `compile-metrics.json` | Per-package compile command, wall time, exit code, binary size, and log paths. |
| `test-binaries/` / `compile-logs/` | Native test executables and their stdout/stderr. |
| `clean-*.log`, `go-version*`, `repository-sha*` | Setup/contract evidence. |
| `go-test-summary.json` / `.txt` | Parser-created suite, package, top-level test/subtest timing, status, and diagnostics. |

The parser JSON schema identifier is `ci-bench.go-test-json.v1`. The PowerShell
metadata and resource records currently use numeric `schemaVersion: 1`.

## Azure create, run, retain, delete

The Azure helpers deliberately create one disposable, non-spot Windows VM in
the exact group `rg-entire-win-ci-<agent>-<run-id>`. Resources are tagged with
`purpose`, `agent`, `run`, `expires`, and `owner`; the VM has a Premium SSD OS
disk, no public IP, and no custom NSG rules. Creation refuses an existing group.

First discover an available region, image, and Premium-IO VM SKU instead of
assuming a SKU exists:

```bash
az account list-locations --output table
az vm list-skus --location westus3 --resource-type virtualMachines --output table
az vm image list --location westus3 --publisher MicrosoftWindowsServer --offer WindowsServer --all --output table
```

Then validate and create. Replace the example SKU if discovery shows it is
restricted:

```bash
agent=a1
run_id=a1-202609020545
location=westus3
vm_size=Standard_D8ds_v5

tools/ci-bench/create-azure-vm.sh \
  --agent "$agent" --run-id "$run_id" --location "$location" \
  --vm-size "$vm_size" --validate-only

tools/ci-bench/create-azure-vm.sh \
  --agent "$agent" --run-id "$run_id" --location "$location" \
  --vm-size "$vm_size"
```

The create helper provisions infrastructure only. It does not install
PowerShell 7, Go, Git, GCC, Python, clone the repository, or export artifacts.
Prepare one reviewed, self-contained PowerShell bootstrap/driver that stages
those prerequisites, checks out the pinned product commit, invokes
`run-go-suite.ps1` under `pwsh`, and exports the result archive without printing
credentials. Run Command transfers exactly that one `.ps1`; it accepts no
inline or protected arguments.

Validate and execute the prepared driver, retaining the Azure response locally:

```bash
driver=results/windows-ci/a1/run-a1.ps1
response=results/windows-ci/a1/azure-run-command-response.json

tools/ci-bench/run-on-azure-vm.sh \
  --agent "$agent" --run-id "$run_id" --script "$driver" --validate-only

tools/ci-bench/run-on-azure-vm.sh \
  --agent "$agent" --run-id "$run_id" --script "$driver" \
  --output-file "$response"
```

`--output-file` is only the Azure Run Command response; it does not download the
remote result directory. Before deletion, export the complete result directory
to approved durable artifact storage, verify its archive/checksum locally, and
place or link it under `results/windows-ci/<agent>/<run-id>/`. Review artifacts
for secrets. Do not attach this VM as a persistent GitHub runner.

After artifact verification, delete the exact group and wait for confirmation:

```bash
tools/ci-bench/delete-azure-resources.sh \
  --agent "$agent" --run-id "$run_id" --validate-only

tools/ci-bench/delete-azure-resources.sh \
  --agent "$agent" --run-id "$run_id" --yes

az group exists --name "rg-entire-win-ci-${agent}-${run_id}" --output tsv
```

The final command must print `false`. Avoid `--no-wait` unless another process
will wait and verify deletion. The delete helper refuses arbitrary group names,
wildcards, tag mismatches, or deletion without `--yes`; a missing exact group is
an idempotent success.

## Mandatory final cleanup sweep

The investigation is incomplete while any tagged benchmark resource remains.
After all agents finish, list tagged resources, resolve every exact agent/run
pair through the guarded delete helper, and repeat until the listing is empty.
Record the final evidence:

```bash
mkdir -p results/windows-ci
{
  date -u '+checked_at=%Y-%m-%dT%H:%M:%SZ'
  az resource list \
    --tag purpose=entire-windows-ci-benchmark \
    --query '[].{resourceGroup:resourceGroup,name:name,type:type}' \
    --output tsv
} | tee results/windows-ci/resource-cleanup.txt
```

An empty resource listing after the timestamp is the required clean result.
Never replace the exact, tag-checked deletion helper with a broad prefix or
wildcard deletion.
