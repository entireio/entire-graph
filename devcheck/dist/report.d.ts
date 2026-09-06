/**
 * DevCheck — Report Formatting
 *
 * Turns raw Graph output into human-readable, evidence-based reports.
 * All language hedges — "may be affected", "possible impact" — because
 * Graph evidence is not absolute truth.
 */
import type { ScanResult, ImpactReport, AffectedItem, RecommendedCheck, Confidence } from "./types.js";
export declare function formatScanReport(scan: ScanResult): string;
export declare function formatImpactReport(report: ImpactReport): string;
/**
 * Parse the text output of `entire graph impact` into structured data.
 * The format typically looks like:
 *
 *   Impact: SymbolName (file.ts:10) ...
 *   Blast radius: N caller ...
 *   Callers (N direct, M transitive ...):
 *   - CallerName (file.ts:20, def :15)
 *   Callees (...):
 *   - CalleeName (file.ts:30)
 *   ...
 */
export declare function parseImpactOutput(raw: string, targetSymbol: string): {
    affectedItems: AffectedItem[];
    summary: string;
    confidence: Confidence;
    warnings: string[];
};
/**
 * Try to extract a canonical symbol name from graph search JSON output.
 */
export declare function extractSymbolFromSearch(searchOutput: string): string | null;
export declare function buildRecommendedChecks(affectedItems: AffectedItem[], target: string): RecommendedCheck[];
