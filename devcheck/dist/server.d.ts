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
export {};
