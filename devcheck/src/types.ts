/**
 * DevCheck Types
 *
 * Shared interfaces for scan results, impact reports, and Graph evidence.
 * All impact language uses hedging — "may be affected", "possible impact" —
 * because Graph evidence is not absolute truth.
 */

// ---------------------------------------------------------------------------
// Confidence
// ---------------------------------------------------------------------------

export type Confidence = "high" | "medium" | "low" | "unknown";

// ---------------------------------------------------------------------------
// Scan
// ---------------------------------------------------------------------------

export interface ScanResult {
  projectPath: string;
  folderExists: boolean;
  graphAvailable: boolean;
  graphVersion: string | null;
  nextStep: string | null;
}

// ---------------------------------------------------------------------------
// Impact report
// ---------------------------------------------------------------------------

export interface GraphEvidence {
  /** The raw `entire graph` command that produced this evidence. */
  command: string;
  /** A short, human-readable summary of what the command returned. */
  summary: string;
  /** The full raw output from the command (trimmed). */
  raw: string;
}

export interface AffectedItem {
  /** File path or symbol name that may be affected. */
  name: string;
  /** Why this item may be affected (caller, importer, type consumer, etc.). */
  reason: string;
  /** The relationship kind from Graph (e.g. "caller", "type-consumer"). */
  relationship: string;
  /** Source location if known (e.g. "mux.go:203"). */
  location: string | null;
}

export interface RecommendedCheck {
  /** A human-readable action the developer should take. */
  action: string;
  /** Why this check matters. */
  why: string;
}

export interface ImpactReport {
  /** The target file, function, or symbol that was analyzed. */
  target: string;
  /** One-paragraph plain-English summary. */
  summary: string;
  /** Items that may be affected by a change to the target. */
  affectedItems: AffectedItem[];
  /** Suggested tests or manual checks. */
  recommendedChecks: RecommendedCheck[];
  /** The Graph evidence backing this report. */
  evidence: GraphEvidence[];
  /** Overall confidence in the analysis. */
  confidence: Confidence;
  /** Any warnings about incomplete or uncertain results. */
  warnings: string[];
}

// ---------------------------------------------------------------------------
// Graph command results (internal)
// ---------------------------------------------------------------------------

export interface GraphCommandResult {
  success: boolean;
  stdout: string;
  stderr: string;
  exitCode: number | null;
}
