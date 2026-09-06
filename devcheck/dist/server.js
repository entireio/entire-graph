/**
 * DevCheck — MCP Server
 *
 * A read-only MCP tool that uses Entire Graph to analyze code change impact.
 * Exposes two tools:
 *   1. scan_project   — check if a project is ready for Graph analysis
 *   2. check_change_impact — analyze what might be affected by a code change
 *
 * This server NEVER modifies files. It inspects code and produces reports.
 */
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { z } from "zod";
import * as fs from "node:fs";
import { checkGraphAvailable, graphDoctor, graphSearch, graphImpact, graphDef, } from "./graph.js";
import { formatScanReport, formatImpactReport, parseImpactOutput, extractSymbolFromSearch, buildRecommendedChecks, } from "./report.js";
// ---------------------------------------------------------------------------
// Server setup
// ---------------------------------------------------------------------------
const server = new McpServer({
    name: "devcheck",
    version: "1.0.0",
});
// ---------------------------------------------------------------------------
// Tool 1: scan_project
// ---------------------------------------------------------------------------
server.tool("scan_project", "Check whether a project folder exists and whether Entire Graph is available and ready for analysis. Returns a status report with clear next steps if setup is incomplete.", { project_path: z.string().describe("Absolute path to the project repository folder") }, async ({ project_path }) => {
    const scanResult = {
        projectPath: project_path,
        folderExists: false,
        graphAvailable: false,
        graphVersion: null,
        nextStep: null,
    };
    // 1. Check folder exists
    try {
        const stats = fs.statSync(project_path);
        scanResult.folderExists = stats.isDirectory();
    }
    catch {
        scanResult.folderExists = false;
        scanResult.nextStep = "The specified path does not exist or is not accessible. Check the path and try again.";
        return { content: [{ type: "text", text: formatScanReport(scanResult) }] };
    }
    if (!scanResult.folderExists) {
        scanResult.nextStep = "The specified path is not a directory.";
        return { content: [{ type: "text", text: formatScanReport(scanResult) }] };
    }
    // 2. Check Graph availability
    const graphCheck = await checkGraphAvailable();
    scanResult.graphAvailable = graphCheck.available;
    scanResult.graphVersion = graphCheck.version;
    if (!graphCheck.available) {
        scanResult.nextStep =
            "Entire Graph is not available. Install the Entire CLI and the graph plugin: `entire plugin install graph`";
        return { content: [{ type: "text", text: formatScanReport(scanResult) }] };
    }
    // 3. Check if graph works for this repo
    const doctorResult = await graphDoctor(project_path);
    if (!doctorResult.success) {
        scanResult.nextStep =
            "Entire Graph is installed but could not analyze this repository. " +
                "Run `entire graph init-agents --repo .` inside the repository, then try again.";
        return { content: [{ type: "text", text: formatScanReport(scanResult) }] };
    }
    scanResult.nextStep = "Ready! Use the `check_change_impact` tool to analyze a target.";
    return { content: [{ type: "text", text: formatScanReport(scanResult) }] };
});
// ---------------------------------------------------------------------------
// Tool 2: check_change_impact
// ---------------------------------------------------------------------------
server.tool("check_change_impact", "Analyze the potential impact of changing a file, function, or symbol in a code repository. Uses Entire Graph to find callers, dependents, type consumers, and related tests, then produces a structured change-safety report.", {
    project_path: z.string().describe("Absolute path to the project repository folder"),
    target: z.string().describe("A file path, function name, or symbol name to analyze"),
}, async ({ project_path, target }) => {
    // 1. Validate folder
    try {
        const stats = fs.statSync(project_path);
        if (!stats.isDirectory()) {
            return {
                content: [{
                        type: "text",
                        text: `❌ Error: \`${project_path}\` is not a directory. Please provide a valid project path.`,
                    }],
            };
        }
    }
    catch {
        return {
            content: [{
                    type: "text",
                    text: `❌ Error: \`${project_path}\` does not exist or is not accessible.`,
                }],
        };
    }
    // 2. Check Graph is available
    const graphCheck = await checkGraphAvailable();
    if (!graphCheck.available) {
        return {
            content: [{
                    type: "text",
                    text: "❌ Error: Entire Graph is not available.\n\n" +
                        "Install it with:\n" +
                        "1. Install Entire CLI: see https://docs.entire.io/installation\n" +
                        "2. Install graph plugin: `entire plugin install graph`\n" +
                        "3. Enable in repo: `entire graph init-agents --repo .`",
                }],
        };
    }
    // 3. Collect evidence
    const evidence = [];
    const warnings = [];
    // --- Step 3a: Search for the target ---
    let resolvedSymbol = target;
    const searchResult = await graphSearch(project_path, target);
    if (searchResult.success && searchResult.stdout) {
        evidence.push({
            command: `entire graph search --repo "${project_path}" --query "${target}" --profile full --format json`,
            summary: "Graph search for the target",
            raw: searchResult.stdout.slice(0, 3000), // trim for readability
        });
        // Try to extract a better symbol name from search results
        const extracted = extractSymbolFromSearch(searchResult.stdout);
        if (extracted) {
            resolvedSymbol = extracted;
        }
    }
    else {
        warnings.push(`Graph search did not return results for "${target}". Falling back to using the target name directly.`);
    }
    // --- Step 3b: Run impact analysis ---
    const impactResult = await graphImpact(project_path, resolvedSymbol);
    if (impactResult.success && impactResult.stdout) {
        evidence.push({
            command: `entire graph impact --repo "${project_path}" --symbol "${resolvedSymbol}" --depth 2`,
            summary: "Impact analysis showing callers, callees, type consumers, and data flows",
            raw: impactResult.stdout.slice(0, 4000),
        });
    }
    else if (impactResult.stderr) {
        warnings.push(`Graph impact analysis returned an error: ${impactResult.stderr.slice(0, 500)}`);
    }
    // --- Step 3c: Get definition ---
    const defResult = await graphDef(project_path, resolvedSymbol);
    if (defResult.success && defResult.stdout) {
        evidence.push({
            command: `entire graph def "${resolvedSymbol}" --repo "${project_path}"`,
            summary: "Symbol definition lookup",
            raw: defResult.stdout.slice(0, 2000),
        });
    }
    // 4. Parse impact output into structured data
    const impactRaw = impactResult.success ? impactResult.stdout : "";
    const parsed = parseImpactOutput(impactRaw, resolvedSymbol);
    // 5. Build recommended checks
    const recommendedChecks = buildRecommendedChecks(parsed.affectedItems, resolvedSymbol);
    // 6. Assemble the full report
    const report = {
        target: resolvedSymbol,
        summary: parsed.summary,
        affectedItems: parsed.affectedItems,
        recommendedChecks,
        evidence,
        confidence: parsed.confidence,
        warnings: [...parsed.warnings, ...warnings],
    };
    return { content: [{ type: "text", text: formatImpactReport(report) }] };
});
// ---------------------------------------------------------------------------
// Start
// ---------------------------------------------------------------------------
async function main() {
    const transport = new StdioServerTransport();
    await server.connect(transport);
}
main().catch((err) => {
    console.error("DevCheck MCP server failed to start:", err);
    process.exit(1);
});
//# sourceMappingURL=server.js.map