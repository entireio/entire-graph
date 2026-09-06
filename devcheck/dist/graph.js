/**
 * DevCheck — Entire Graph CLI Wrappers
 *
 * Pure wrappers around `entire graph` commands.
 * Each function executes a single CLI command and returns the raw result.
 * No interpretation or formatting happens here — that belongs in report.ts.
 */
import { execFile } from "node:child_process";
import { promisify } from "node:util";
const execFileAsync = promisify(execFile);
const ENTIRE_BIN = "entire";
const TIMEOUT_MS = 60_000;
// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
async function runGraph(args, cwd) {
    try {
        const { stdout, stderr } = await execFileAsync(ENTIRE_BIN, ["graph", ...args], {
            timeout: TIMEOUT_MS,
            maxBuffer: 4 * 1024 * 1024,
            cwd,
            windowsHide: true,
        });
        return { success: true, stdout: stdout.trim(), stderr: stderr.trim(), exitCode: 0 };
    }
    catch (err) {
        const e = err;
        // Command ran but returned non-zero
        if (e.stdout !== undefined) {
            return {
                success: false,
                stdout: (e.stdout ?? "").trim(),
                stderr: (e.stderr ?? "").trim(),
                exitCode: typeof e.code === "number" ? e.code : 1,
            };
        }
        // Command not found or other system error
        return {
            success: false,
            stdout: "",
            stderr: e.message ?? String(err),
            exitCode: null,
        };
    }
}
// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------
/**
 * Check whether the Entire CLI and the graph plugin are installed.
 * Returns the version string on success, or null if unavailable.
 */
export async function checkGraphAvailable() {
    const result = await runGraph(["version"]);
    if (result.success) {
        return { available: true, version: result.stdout, error: null };
    }
    return { available: false, version: null, error: result.stderr };
}
/**
 * Run `entire graph doctor` to diagnose the Graph environment.
 */
export async function graphDoctor(repo) {
    return runGraph(["doctor", "--json"], repo);
}
/**
 * Search the codebase for a plain-language query or symbol name.
 * Uses `--profile full` to activate the call graph.
 */
export async function graphSearch(repo, query) {
    return runGraph([
        "search",
        "--repo", repo,
        "--query", query,
        "--profile", "full",
        "--format", "json",
        "--top-k", "10",
    ], repo);
}
/**
 * Show callers, callees, type consumers, data flows, related files,
 * and same-container symbols for one symbol.
 */
export async function graphImpact(repo, symbol) {
    return runGraph([
        "impact",
        "--repo", repo,
        "--symbol", symbol,
        "--depth", "2",
    ], repo);
}
/**
 * Show a symbol's declaration (type, fields, methods, implementations).
 */
export async function graphDef(repo, symbol) {
    return runGraph(["def", symbol, "--repo", repo], repo);
}
/**
 * List entity-level changes between two Git references.
 */
export async function graphDiff(repo, base, head) {
    return runGraph([
        "diff",
        "--base", base,
        "--head", head,
        "--repo", repo,
    ], repo);
}
/**
 * Show direct relationships for one symbol.
 */
export async function graphNeighbors(repo, symbol, relation = "CALLS", direction = "in") {
    return runGraph([
        "neighbors",
        "--repo", repo,
        "--symbol", symbol,
        "--relation", relation,
        "--direction", direction,
    ], repo);
}
//# sourceMappingURL=graph.js.map