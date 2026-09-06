# DevCheck

**If I change this file or function, what else might be affected, why, and which tests should I run?**

DevCheck is a local MCP tool that uses [Entire Graph](https://docs.entire.io/guides/graph/overview) to analyze code change impact. It inspects your codebase, finds code relationships, and produces a readable change-safety report — all without modifying any files.

## Prerequisites

- [Node.js](https://nodejs.org/) v18 or later
- [Entire CLI](https://docs.entire.io/installation) v0.10.0 or later
- The `entire-graph` plugin: `entire plugin install graph`
- A Git repository with `entire graph init-agents --repo .` already run

## Quick Start

```bash
# 1. Install dependencies
cd devcheck
npm install

# 2. Build
npm run build

# 3. Run tests
npm test

# 4. Start the MCP server (for direct testing)
npm start
```

## Configuring in Antigravity

The MCP server is pre-configured in [`.agents/mcp_config.json`](../.agents/mcp_config.json):

```json
{
  "mcpServers": {
    "devcheck": {
      "command": "node",
      "args": ["devcheck/dist/server.js"],
      "cwd": "."
    }
  }
}
```

Restart Antigravity to pick up the new MCP server.

## Tools

### `scan_project`

Check whether a project is ready for Graph analysis.

**Input:**
```json
{ "project_path": "/absolute/path/to/repo" }
```

**Returns:** A status report showing:
- Whether the folder exists
- Whether Entire Graph is installed and ready
- Clear next steps if setup is incomplete

### `check_change_impact`

Analyze the potential impact of changing a target.

**Input:**
```json
{
  "project_path": "/absolute/path/to/repo",
  "target": "functionName"
}
```

**Returns:** A structured impact report with:
- Plain-English summary
- Possible affected files/functions with explanations
- Recommended tests and checks
- Raw Graph evidence backing each finding
- Confidence level and warnings
- A reminder to verify findings against source code

## Example Usage

In an Antigravity chat, ask:

```
Use devcheck to check what might be affected if I change the formatImpactReport function
```

The agent will call `check_change_impact` and return a formatted report.

## How It Works

```
User prompt → Antigravity → DevCheck MCP Server
                                │
                    ┌───────────┼───────────┐
                    │           │           │
              scan_project  check_change_impact
                    │           │
                    ▼           ▼
               graph.ts     graph.ts
              (version,    (search, impact,
               doctor)      def, diff)
                    │           │
                    ▼           ▼
              report.ts     report.ts
             (scan fmt)   (impact fmt)
                    │           │
                    └───────────┘
                        │
                   Formatted Report
```

1. **graph.ts** wraps `entire graph` CLI commands — pure command execution, no interpretation
2. **report.ts** parses Graph output and formats readable reports with evidence and hedging language
3. **server.ts** orchestrates the MCP tools, calling graph.ts and report.ts

## Important: Evidence, Not Truth

DevCheck treats Graph results as **evidence, not absolute truth**:

- All findings use hedging language: "may be affected", "possible impact"
- Every report includes the raw Graph commands and output that support each finding
- Every report ends with a verification reminder
- When Graph evidence is incomplete, the report clearly says so

## Entire Graph Commands Used

| Command | Purpose |
|---------|---------|
| `entire graph version` | Check if Graph plugin is installed |
| `entire graph doctor` | Diagnose the Graph environment |
| `entire graph search` | Find relevant code regions |
| `entire graph impact` | Show callers, callees, type consumers, data flows |
| `entire graph def` | Look up symbol definitions |
| `entire graph diff` | Semantic diff between Git refs |

## Development

```bash
# Watch mode for development
npm run dev

# Run tests
npm test
```

## Project Structure

```
devcheck/
├── package.json        # Dependencies and scripts
├── tsconfig.json       # TypeScript configuration
├── src/
│   ├── types.ts        # Shared TypeScript interfaces
│   ├── graph.ts        # Entire Graph CLI wrappers
│   ├── report.ts       # Report formatting and parsing
│   └── server.ts       # MCP server entry point
└── tests/
    └── report.test.ts  # Unit tests for formatting and parsing
```
