/**
 * DevCheck — Unit Tests for Report Formatting
 *
 * Tests the report formatter and parser functions.
 * These tests run without the Entire CLI installed because they
 * only exercise formatting and parsing logic, not CLI execution.
 */

import { describe, it, expect } from "vitest";
import {
  formatScanReport,
  formatImpactReport,
  parseImpactOutput,
  extractSymbolFromSearch,
  buildRecommendedChecks,
} from "../src/report.js";
import type { ScanResult, ImpactReport, AffectedItem } from "../src/types.js";

// ---------------------------------------------------------------------------
// formatScanReport
// ---------------------------------------------------------------------------

describe("formatScanReport", () => {
  it("shows error when folder does not exist", () => {
    const scan: ScanResult = {
      projectPath: "/fake/path",
      folderExists: false,
      graphAvailable: false,
      graphVersion: null,
      nextStep: null,
    };
    const report = formatScanReport(scan);
    expect(report).toContain("❌ No");
    expect(report).toContain("/fake/path");
    expect(report).toContain("does not exist");
  });

  it("shows setup instructions when Graph is not available", () => {
    const scan: ScanResult = {
      projectPath: "/real/project",
      folderExists: true,
      graphAvailable: false,
      graphVersion: null,
      nextStep: "Install Entire Graph",
    };
    const report = formatScanReport(scan);
    expect(report).toContain("❌ No");
    expect(report).toContain("entire plugin install graph");
    expect(report).toContain("How to set up Entire Graph");
  });

  it("shows ready status when Graph is available", () => {
    const scan: ScanResult = {
      projectPath: "/real/project",
      folderExists: true,
      graphAvailable: true,
      graphVersion: "v1.2.3",
      nextStep: "Ready!",
    };
    const report = formatScanReport(scan);
    expect(report).toContain("✅ Yes");
    expect(report).toContain("v1.2.3");
    expect(report).not.toContain("How to set up Entire Graph");
  });
});

// ---------------------------------------------------------------------------
// parseImpactOutput
// ---------------------------------------------------------------------------

describe("parseImpactOutput", () => {
  it("returns empty results for empty input", () => {
    const result = parseImpactOutput("", "myFunc");
    expect(result.affectedItems).toHaveLength(0);
    expect(result.confidence).toBe("unknown");
    expect(result.warnings.length).toBeGreaterThan(0);
    expect(result.summary).toContain("Manual review");
  });

  it("parses callers from impact output", () => {
    const raw = `Impact: myFunc (src/auth.ts:10) def=10 span=10-25 [function]
Blast radius: 2 callers (2 direct, 0 transitive), 1 callee, 0 type consumers.
Callers (2 direct, 0 transitive; who breaks if behavior changes):
- handleLogin (src/routes/login.ts:42, def :38)
- handleRegister (src/routes/register.ts:15, def :10)
Callees (1):
- hashPassword (src/utils/hash.ts:5, def :3)`;

    const result = parseImpactOutput(raw, "myFunc");
    expect(result.affectedItems).toHaveLength(3);

    // Check callers
    const callers = result.affectedItems.filter((i) => i.relationship === "caller");
    expect(callers).toHaveLength(2);
    expect(callers[0].name).toBe("handleLogin");
    expect(callers[0].location).toContain("login.ts");

    // Check callees
    const callees = result.affectedItems.filter((i) => i.relationship === "callee");
    expect(callees).toHaveLength(1);
    expect(callees[0].name).toBe("hashPassword");

    // Check confidence is high (>= 5 items not met, so medium)
    expect(result.confidence).toBe("medium");
  });

  it("parses type consumers and data flows", () => {
    const raw = `Impact: UserSession (src/types.ts:5)
Blast radius: 0 callers, 0 callees, 2 type consumers, 1 data flow.
Type consumers (2):
- AuthMiddleware (src/middleware/auth.ts:20)
- SessionStore (src/store/session.ts:8)
Data flow (1):
- createToken (src/auth/token.ts:15)`;

    const result = parseImpactOutput(raw, "UserSession");
    expect(result.affectedItems).toHaveLength(3);

    const typeConsumers = result.affectedItems.filter((i) => i.relationship === "type consumer");
    expect(typeConsumers).toHaveLength(2);

    const dataFlows = result.affectedItems.filter((i) => i.relationship === "data flow");
    expect(dataFlows).toHaveLength(1);
  });

  it("sets high confidence when many items are found", () => {
    const lines = [
      "Impact: Router.Match (mux.go:151)",
      "Blast radius: 6 callers, 2 callees.",
      "Callers (6 direct):",
      "- A (a.go:1)",
      "- B (b.go:2)",
      "- C (c.go:3)",
      "- D (d.go:4)",
      "- E (e.go:5)",
      "- F (f.go:6)",
    ];
    const result = parseImpactOutput(lines.join("\n"), "Router.Match");
    expect(result.affectedItems.length).toBeGreaterThanOrEqual(5);
    expect(result.confidence).toBe("high");
  });

  it("warns on cache miss", () => {
    const raw = `Index: cache-miss (250ms)
Impact: foo (bar.ts:1)
Blast radius: 0 callers.`;
    const result = parseImpactOutput(raw, "foo");
    expect(result.warnings.some((w) => w.includes("cold cache"))).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// extractSymbolFromSearch
// ---------------------------------------------------------------------------

describe("extractSymbolFromSearch", () => {
  it("extracts symbol from JSON array result", () => {
    const json = JSON.stringify([{ symbol: "Router.Match", file: "mux.go", line: 151 }]);
    expect(extractSymbolFromSearch(json)).toBe("Router.Match");
  });

  it("extracts from results wrapper", () => {
    const json = JSON.stringify({ results: [{ name: "validateToken", path: "auth.ts" }] });
    expect(extractSymbolFromSearch(json)).toBe("validateToken");
  });

  it("returns null for unparseable input", () => {
    expect(extractSymbolFromSearch("")).toBeNull();
    expect(extractSymbolFromSearch("no json here")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// buildRecommendedChecks
// ---------------------------------------------------------------------------

describe("buildRecommendedChecks", () => {
  it("recommends reviewing callers when callers exist", () => {
    const items: AffectedItem[] = [
      { name: "handleLogin", reason: "calls target", relationship: "caller", location: "login.ts:10" },
      { name: "handleRegister", reason: "calls target", relationship: "caller", location: "register.ts:5" },
    ];
    const checks = buildRecommendedChecks(items, "authenticate");
    expect(checks.some((c) => c.action.includes("caller"))).toBe(true);
    expect(checks.some((c) => c.action.includes("2"))).toBe(true);
  });

  it("recommends running test suite when test files found", () => {
    const items: AffectedItem[] = [
      { name: "authTest", reason: "calls target", relationship: "caller", location: "auth.test.ts:10" },
    ];
    const checks = buildRecommendedChecks(items, "login");
    expect(checks.some((c) => c.action.toLowerCase().includes("test suite"))).toBe(true);
  });

  it("always includes manual search and full test suite recommendations", () => {
    const checks = buildRecommendedChecks([], "anything");
    expect(checks.some((c) => c.action.includes("Search for usages"))).toBe(true);
    expect(checks.some((c) => c.action.includes("full project test suite"))).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// formatImpactReport
// ---------------------------------------------------------------------------

describe("formatImpactReport", () => {
  it("uses hedging language throughout", () => {
    const report: ImpactReport = {
      target: "processPayment",
      summary: "Changing `processPayment` may affect payment processing.",
      affectedItems: [
        {
          name: "handleCheckout",
          reason: "`handleCheckout` calls `processPayment` and may break.",
          relationship: "caller",
          location: "checkout.ts:20",
        },
      ],
      recommendedChecks: [{ action: "Run payment tests", why: "Connected to target" }],
      evidence: [{
        command: "entire graph impact --symbol processPayment",
        summary: "Impact analysis",
        raw: "Callers: handleCheckout",
      }],
      confidence: "medium",
      warnings: [],
    };
    const output = formatImpactReport(report);

    // Check hedging
    expect(output).toContain("may");
    expect(output).toContain("Possible Affected Areas");

    // Check structure
    expect(output).toContain("Change Impact Report");
    expect(output).toContain("processPayment");
    expect(output).toContain("Recommended Checks");
    expect(output).toContain("Graph Evidence");
    expect(output).toContain("Verification Required");
  });

  it("shows empty state when no items found", () => {
    const report: ImpactReport = {
      target: "orphanFunc",
      summary: "No relationships found.",
      affectedItems: [],
      recommendedChecks: [],
      evidence: [],
      confidence: "low",
      warnings: ["Graph found nothing."],
    };
    const output = formatImpactReport(report);
    expect(output).toContain("No direct relationships found");
    expect(output).toContain("Manual review");
    expect(output).toContain("Warnings");
  });

  it("always includes the verification reminder", () => {
    const report: ImpactReport = {
      target: "x",
      summary: "test",
      affectedItems: [],
      recommendedChecks: [],
      evidence: [],
      confidence: "unknown",
      warnings: [],
    };
    const output = formatImpactReport(report);
    expect(output).toContain("Verification Required");
    expect(output).toContain("not absolute truth");
  });
});
