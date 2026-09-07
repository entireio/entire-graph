# Graph Parser System Documentation

This directory is a chapter-by-chapter, PDF-ready description of the Graph
Parser System (GPS) implemented by Entire Graph. Read the files in numeric
order.

## Build One PDF

With Pandoc installed, build the manuscript from the repository root:

```sh
pandoc --toc --number-sections --metadata title="Graph Parser System" \
  GRAPH_PARSER_SYSTEM/00-front-matter.md \
  GRAPH_PARSER_SYSTEM/01-problem-and-solution.md \
  GRAPH_PARSER_SYSTEM/02-theory-of-evidence.md \
  GRAPH_PARSER_SYSTEM/03-architecture-and-data-model.md \
  GRAPH_PARSER_SYSTEM/04-decision-workflows.md \
  GRAPH_PARSER_SYSTEM/05-implementation-guide.md \
  GRAPH_PARSER_SYSTEM/06-user-verification-guide.md \
  GRAPH_PARSER_SYSTEM/07-requirement-traceability.md \
  GRAPH_PARSER_SYSTEM/08-limitations-and-roadmap.md \
  -o GRAPH_PARSER_SYSTEM/graph-parser-system.pdf
```

The generated PDF is intentionally not tracked. Each chapter is also useful as
an independent Markdown document.

## Source Basis

This documentation describes the current repository-local GPS implementation,
principally `internal/cli/gps.go` and `internal/intent/`. Existing detailed
design records remain in `docs/GPS/`; this directory is the consolidated
problem-statement response and implementation guide.
