/**
 * DevCheck — Report Formatting
 *
 * Turns raw Graph output into human-readable, evidence-based reports.
 * All language hedges — "may be affected", "possible impact" — because
 * Graph evidence is not absolute truth.
 */

import type {
  ScanResult,
  ImpactReport,
  AffectedItem,
  RecommendedCheck,
  GraphEvidence,
  Confidence,
} from "./types.js";

// ---------------------------------------------------------------------------
// Scan report
// ---------------------------------------------------------------------------

export function formatScanReport(scan: ScanResult): string {
  const lines: string[] = [];
  lines.push("# Project Scan Report");
  lines.push("");
  lines.push(`**Project path:** \`${scan.projectPath}\``);
  lines.push(`**Folder exists:** ${scan.folderExists ? "✅ Yes" : "❌ No"}`);

  if (!scan.folderExists) {
    lines.push("");
    lines.push("> ⚠️ The specified folder does not exist. Please check the path and try again.");
    return lines.join("\n");
  }

  lines.push(`**Entire Graph available:** ${scan.graphAvailable ? "✅ Yes" : "❌ No"}`);

  if (scan.graphVersion) {
    lines.push(`**Graph version:** ${scan.graphVersion}`);
  }

  if (scan.nextStep) {
    lines.push("");
    lines.push(`**Next step:** ${scan.nextStep}`);
  }

  if (!scan.graphAvailable) {
    lines.push("");
    lines.push("### How to set up Entire Graph");
    lines.push("");
    lines.push("1. Install the Entire CLI: see https://docs.entire.io/installation");
    lines.push("2. Install the graph plugin: `entire plugin install graph`");
    lines.push("3. Enable graph in this repository: `entire graph init-agents --repo .`");
    lines.push("4. Re-run `scan_project` to verify.");
  }

  return lines.join("\n");
}

// ---------------------------------------------------------------------------
// Impact report
// ---------------------------------------------------------------------------

export function formatImpactReport(report: ImpactReport): string {
  const lines: string[] = [];

  // Header
  lines.push("# Change Impact Report");
  lines.push("");
  lines.push(`**Target:** \`${report.target}\``);
  lines.push(`**Confidence:** ${formatConfidence(report.confidence)}`);
  lines.push("");

  // Summary
  lines.push("## Summary");
  lines.push("");
  lines.push(report.summary);
  lines.push("");

  // Affected areas
  if (report.affectedItems.length > 0) {
    lines.push("## Possible Affected Areas");
    lines.push("");
    for (const item of report.affectedItems) {
      const loc = item.location ? ` (${item.location})` : "";
      lines.push(`- **\`${item.name}\`**${loc}`);
      lines.push(`  - Relationship: ${item.relationship}`);
      lines.push(`  - Why: ${item.reason}`);
    }
    lines.push("");
  } else {
    lines.push("## Possible Affected Areas");
    lines.push("");
    lines.push("No direct relationships found by Graph. This may mean:");
    lines.push("- The target has no callers or dependents in the analyzed scope");
    lines.push("- Graph coverage does not include this target's language or file type");
    lines.push("- Manual review is recommended");
    lines.push("");
  }

  // Recommended checks
  if (report.recommendedChecks.length > 0) {
    lines.push("## Recommended Checks");
    lines.push("");
    for (const check of report.recommendedChecks) {
      lines.push(`- **${check.action}**`);
      lines.push(`  - ${check.why}`);
    }
    lines.push("");
  }

  // Evidence
  if (report.evidence.length > 0) {
    lines.push("## Graph Evidence");
    lines.push("");
    lines.push("The following Entire Graph commands provided the evidence for this report:");
    lines.push("");
    for (let i = 0; i < report.evidence.length; i++) {
      const ev = report.evidence[i];
      lines.push(`### Evidence ${i + 1}: ${ev.summary}`);
      lines.push("");
      lines.push(`**Command:** \`${ev.command}\``);
      lines.push("");
      lines.push("```");
      lines.push(ev.raw);
      lines.push("```");
      lines.push("");
    }
  }

  // Warnings
  if (report.warnings.length > 0) {
    lines.push("## ⚠️ Warnings");
    lines.push("");
    for (const w of report.warnings) {
      lines.push(`- ${w}`);
    }
    lines.push("");
  }

  // Verification reminder — always present
  lines.push("---");
  lines.push("");
  lines.push("**⚠️ Verification Required**");
  lines.push("");
  lines.push(
    "This report is based on Entire Graph's structural analysis. " +
    "Graph evidence is not absolute truth. " +
    "Please verify each finding against the actual source code and run relevant tests before making changes."
  );
  lines.push("");

  return lines.join("\n");
}

// ---------------------------------------------------------------------------
// Impact parsing helpers
// ---------------------------------------------------------------------------

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
export function parseImpactOutput(
  raw: string,
  targetSymbol: string,
): {
  affectedItems: AffectedItem[];
  summary: string;
  confidence: Confidence;
  warnings: string[];
} {
  const affectedItems: AffectedItem[] = [];
  const warnings: string[] = [];
  let summary = `Changing \`${targetSymbol}\` may affect the items listed below.`;
  let confidence: Confidence = "medium";

  if (!raw || raw.trim().length === 0) {
    return {
      affectedItems: [],
      summary: `No Graph impact data found for \`${targetSymbol}\`. Manual review is recommended.`,
      confidence: "unknown",
      warnings: ["Entire Graph returned no output for this target. The symbol may not exist in the graph index, or the language may not be supported."],
    };
  }

  const lines = raw.split("\n");

  // Extract blast radius summary line
  const blastLine = lines.find((l) => l.toLowerCase().includes("blast radius"));
  if (blastLine) {
    summary = `Changing \`${targetSymbol}\` may affect nearby code. ${blastLine.trim()}`;
  }

  // Current section tracker
  let currentSection = "";
  let currentRelationship = "";

  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed) continue;

    // Detect section headers
    if (trimmed.startsWith("Callers")) {
      currentSection = "callers";
      currentRelationship = "caller";
      continue;
    }
    if (trimmed.startsWith("Callees")) {
      currentSection = "callees";
      currentRelationship = "callee";
      continue;
    }
    if (trimmed.startsWith("Type consumers") || trimmed.startsWith("Type Consumers")) {
      currentSection = "type-consumers";
      currentRelationship = "type consumer";
      continue;
    }
    if (trimmed.startsWith("Data flow") || trimmed.startsWith("Data Flow")) {
      currentSection = "data-flow";
      currentRelationship = "data flow";
      continue;
    }
    if (trimmed.startsWith("Co-change") || trimmed.startsWith("Related files")) {
      currentSection = "co-change";
      currentRelationship = "co-change file";
      continue;
    }
    if (trimmed.startsWith("Siblings") || trimmed.startsWith("Same-container")) {
      currentSection = "siblings";
      currentRelationship = "sibling in same container";
      continue;
    }

    // Parse items (lines starting with "- ")
    if (trimmed.startsWith("- ") && currentSection) {
      const itemText = trimmed.slice(2);
      const match = itemText.match(/^(\S+)\s*\(([^)]+)\)/);
      if (match) {
        const name = match[1];
        const location = match[2];
        affectedItems.push({
          name,
          reason: buildReason(currentRelationship, name, targetSymbol),
          relationship: currentRelationship,
          location,
        });
      } else {
        affectedItems.push({
          name: itemText,
          reason: buildReason(currentRelationship, itemText, targetSymbol),
          relationship: currentRelationship,
          location: null,
        });
      }
    }
  }

  // Set confidence based on data quality
  if (affectedItems.length === 0) {
    confidence = "low";
    warnings.push(
      "Graph found no direct relationships. The target may be a leaf node, or Graph coverage may be incomplete.",
    );
  } else if (affectedItems.length >= 5) {
    confidence = "high";
  }

  // Check for cache miss
  if (raw.includes("cache-miss")) {
    warnings.push(
      "Graph used a cold cache for this query. Run `entire graph index --repo .` for faster future queries.",
    );
  }

  return { affectedItems, summary, confidence, warnings };
}

function buildReason(
  relationship: string,
  itemName: string,
  targetSymbol: string,
): string {
  switch (relationship) {
    case "caller":
      return `\`${itemName}\` calls \`${targetSymbol}\` and may break if its behavior or signature changes.`;
    case "callee":
      return `\`${targetSymbol}\` calls \`${itemName}\`; if the call contract changes, this callee may need updates.`;
    case "type consumer":
      return `\`${itemName}\` uses a type defined or returned by \`${targetSymbol}\` and may be affected by type changes.`;
    case "data flow":
      return `Data flows between \`${itemName}\` and \`${targetSymbol}\`; a change may alter data shape or values.`;
    case "co-change file":
      return `\`${itemName}\` has historically changed alongside \`${targetSymbol}\` and may need a coordinated update.`;
    case "sibling in same container":
      return `\`${itemName}\` is in the same module/class as \`${targetSymbol}\` and may share internal state.`;
    default:
      return `\`${itemName}\` has a \`${relationship}\` relationship with \`${targetSymbol}\` and may be affected.`;
  }
}

// ---------------------------------------------------------------------------
// Search result parsing
// ---------------------------------------------------------------------------

/**
 * Try to extract a canonical symbol name from graph search JSON output.
 */
export function extractSymbolFromSearch(searchOutput: string): string | null {
  try {
    const parsed = JSON.parse(searchOutput);
    // The search results are typically an array of objects with symbol info
    if (Array.isArray(parsed) && parsed.length > 0) {
      const first = parsed[0];
      return first.symbol ?? first.name ?? first.path ?? null;
    }
    if (parsed.results && Array.isArray(parsed.results) && parsed.results.length > 0) {
      const first = parsed.results[0];
      return first.symbol ?? first.name ?? first.path ?? null;
    }
  } catch {
    // If JSON parsing fails, try to extract a symbol from text output
    const match = searchOutput.match(/(?:symbol|name|function|def)\s*[:=]\s*(\S+)/i);
    if (match) return match[1];
  }
  return null;
}

// ---------------------------------------------------------------------------
// Recommended checks builder
// ---------------------------------------------------------------------------

export function buildRecommendedChecks(
  affectedItems: AffectedItem[],
  target: string,
): RecommendedCheck[] {
  const checks: RecommendedCheck[] = [];

  // Check if any callers exist
  const callers = affectedItems.filter((i) => i.relationship === "caller");
  if (callers.length > 0) {
    checks.push({
      action: `Review and test all ${callers.length} caller(s) of \`${target}\``,
      why: "These callers depend on the current behavior and may break if the signature or return values change.",
    });
  }

  // Check for test files
  const testItems = affectedItems.filter(
    (i) =>
      i.name.toLowerCase().includes("test") ||
      i.name.toLowerCase().includes("spec") ||
      (i.location && i.location.toLowerCase().includes("test")),
  );
  if (testItems.length > 0) {
    checks.push({
      action: "Run the related test suite",
      why: `Found ${testItems.length} test-related item(s) connected to the target.`,
    });
  }

  // General recommendation
  checks.push({
    action: `Search for usages of \`${target}\` in the codebase`,
    why: "Graph may not capture every usage. A manual text search can catch dynamic references, string-based lookups, or reflection-based calls.",
  });

  checks.push({
    action: "Run the full project test suite",
    why: "Changes can have indirect effects that Graph analysis may not capture.",
  });

  return checks;
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function formatConfidence(c: Confidence): string {
  switch (c) {
    case "high":
      return "🟢 High — Graph found multiple confirmed relationships";
    case "medium":
      return "🟡 Medium — Graph found some relationships but coverage may be partial";
    case "low":
      return "🔴 Low — Graph found few or no relationships; manual review strongly recommended";
    case "unknown":
      return "⚪ Unknown — Could not determine confidence; manual review required";
  }
}
