/**
 * DevCheck — Entire Graph CLI Wrappers
 *
 * Pure wrappers around `entire graph` commands.
 * Each function executes a single CLI command and returns the raw result.
 * No interpretation or formatting happens here — that belongs in report.ts.
 */
import type { GraphCommandResult } from "./types.js";
/**
 * Check whether the Entire CLI and the graph plugin are installed.
 * Returns the version string on success, or null if unavailable.
 */
export declare function checkGraphAvailable(): Promise<{
    available: boolean;
    version: string | null;
    error: string | null;
}>;
/**
 * Run `entire graph doctor` to diagnose the Graph environment.
 */
export declare function graphDoctor(repo: string): Promise<GraphCommandResult>;
/**
 * Search the codebase for a plain-language query or symbol name.
 * Uses `--profile full` to activate the call graph.
 */
export declare function graphSearch(repo: string, query: string): Promise<GraphCommandResult>;
/**
 * Show callers, callees, type consumers, data flows, related files,
 * and same-container symbols for one symbol.
 */
export declare function graphImpact(repo: string, symbol: string): Promise<GraphCommandResult>;
/**
 * Show a symbol's declaration (type, fields, methods, implementations).
 */
export declare function graphDef(repo: string, symbol: string): Promise<GraphCommandResult>;
/**
 * List entity-level changes between two Git references.
 */
export declare function graphDiff(repo: string, base: string, head: string): Promise<GraphCommandResult>;
/**
 * Show direct relationships for one symbol.
 */
export declare function graphNeighbors(repo: string, symbol: string, relation?: string, direction?: string): Promise<GraphCommandResult>;
