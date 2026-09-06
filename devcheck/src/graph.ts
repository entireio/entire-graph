/**
 * DevCheck — Entire Graph CLI Wrappers
 *
 * Pure wrappers around `entire graph` commands.
 * Each function executes a single CLI command and returns the raw result.
 * No interpretation or formatting happens here — that belongs in report.ts.
 */

import { execFile } from "node:child_process";
import { promisify } from "node:util";
import type { GraphCommandResult } from "./types.js";

const execFileAsync = promisify(execFile);

/** Optional absolute path for MCP hosts whose PATH omits the Entire CLI. */
const ENTIRE_BIN = process.env.ENTIRE_BIN ?? "entire";
const TIMEOUT_MS = 60_000;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

async function runGraph(
  args: string[],
  cwd?: string,
): Promise<GraphCommandResult> {
  try {
    const { stdout, stderr } = await execFileAsync(
      ENTIRE_BIN,
      ["graph", ...args],
      {
        timeout: TIMEOUT_MS,
        maxBuffer: 4 * 1024 * 1024,
        cwd,
        windowsHide: true,
      },
    );
    return { success: true, stdout: stdout.trim(), stderr: stderr.trim(), exitCode: 0 };
  } catch (err: unknown) {
    const e = err as {
      stdout?: string;
      stderr?: string;
      code?: number | string;
      message?: string;
    };

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
export async function checkGraphAvailable(): Promise<{
  available: boolean;
  version: string | null;
  error: string | null;
}> {
  const result = await runGraph(["version"]);
  if (result.success) {
    return { available: true, version: result.stdout, error: null };
  }
  return { available: false, version: null, error: result.stderr };
}

/**
 * Run `entire graph doctor` to diagnose the Graph environment.
 */
export async function graphDoctor(repo: string): Promise<GraphCommandResult> {
  return runGraph(["doctor", "--json"], repo);
}

/**
 * Search the codebase for a plain-language query or symbol name.
 * Uses `--profile full` to activate the call graph.
 */
export async function graphSearch(
  repo: string,
  query: string,
): Promise<GraphCommandResult> {
  return runGraph(
    [
      "search",
      "--repo", repo,
      "--query", query,
      "--profile", "full",
      "--format", "json",
      "--top-k", "10",
    ],
    repo,
  );
}

/**
 * Show callers, callees, type consumers, data flows, related files,
 * and same-container symbols for one symbol.
 */
export async function graphImpact(
  repo: string,
  symbol: string,
): Promise<GraphCommandResult> {
  return runGraph(
    [
      "impact",
      "--repo", repo,
      "--symbol", symbol,
      "--depth", "2",
    ],
    repo,
  );
}

/**
 * Show a symbol's declaration (type, fields, methods, implementations).
 */
export async function graphDef(
  repo: string,
  symbol: string,
): Promise<GraphCommandResult> {
  return runGraph(["def", symbol, "--repo", repo], repo);
}

/**
 * List entity-level changes between two Git references.
 */
export async function graphDiff(
  repo: string,
  base: string,
  head: string,
): Promise<GraphCommandResult> {
  return runGraph(
    [
      "diff",
      "--base", base,
      "--head", head,
      "--repo", repo,
    ],
    repo,
  );
}

/**
 * Show direct relationships for one symbol.
 */
export async function graphNeighbors(
  repo: string,
  symbol: string,
  relation: string = "CALLS",
  direction: string = "in",
): Promise<GraphCommandResult> {
  return runGraph(
    [
      "neighbors",
      "--repo", repo,
      "--symbol", symbol,
      "--relation", relation,
      "--direction", direction,
    ],
    repo,
  );
}
