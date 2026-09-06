import { NextRequest, NextResponse } from "next/server";
import { INITIAL_SYMBOLS, INITIAL_EDGES } from "@/lib/data";

export async function GET(req: NextRequest) {
  const searchParams = req.nextUrl.searchParams;
  const symbol = searchParams.get("symbol") || "";
  const depth = parseInt(searchParams.get("depth") || "2", 10);

  const target = INITIAL_SYMBOLS.find(
    (s) => s.name.toLowerCase() === symbol.toLowerCase() || s.id === symbol
  );

  if (!target) {
    return NextResponse.json(
      { error: `Symbol '${symbol}' not found for impact analysis.` },
      { status: 404 }
    );
  }

  // Find direct callers (incoming edges with CALLS)
  const directCallerEdges = INITIAL_EDGES.filter(
    (e) => e.toId === target.id && e.relation === "CALLS"
  );
  const directCallers = directCallerEdges.map((e) => {
    const sym = INITIAL_SYMBOLS.find((s) => s.id === e.fromId);
    return {
      name: sym ? sym.name : e.fromName,
      path: sym ? sym.path : "unknown",
      line: sym ? sym.startLine : 0,
      callsite: e.callsite,
    };
  });

  // Find transitive callers if depth >= 2
  const transitiveCallers: Array<{ name: string; path: string; line: number; hop: number }> = [];
  if (depth >= 2) {
    for (const caller of directCallerEdges) {
      const hop2Edges = INITIAL_EDGES.filter(
        (e) => e.toId === caller.fromId && e.relation === "CALLS"
      );
      for (const h2 of hop2Edges) {
        const sym = INITIAL_SYMBOLS.find((s) => s.id === h2.fromId);
        if (sym && !directCallers.some((c) => c.name === sym.name)) {
          transitiveCallers.push({
            name: sym.name,
            path: sym.path,
            line: sym.startLine,
            hop: 2,
          });
        }
      }
    }
  }

  // Type consumers
  const typeConsumerEdges = INITIAL_EDGES.filter(
    (e) => e.toId === target.id && (e.relation === "USES_TYPE" || e.relation === "PARAM_TYPE" || e.relation === "RETURNS_TYPE")
  );
  const typeConsumers = typeConsumerEdges.map((e) => {
    const sym = INITIAL_SYMBOLS.find((s) => s.id === e.fromId);
    return {
      name: sym ? sym.name : e.fromName,
      path: sym ? sym.path : "unknown",
      relation: e.relation,
      callsite: e.callsite,
    };
  });

  // Direct callees (outgoing CALLS)
  const directCallees = INITIAL_EDGES.filter(
    (e) => e.fromId === target.id && e.relation === "CALLS"
  ).map((e) => {
    const sym = INITIAL_SYMBOLS.find((s) => s.id === e.toId);
    return {
      name: sym ? sym.name : e.toName,
      path: sym ? sym.path : "unknown",
      callsite: e.callsite,
    };
  });

  // Same container siblings
  const containerSiblings = target.container
    ? INITIAL_SYMBOLS.filter((s) => s.container === target.container && s.id !== target.id).map((s) => ({
        name: s.name,
        kind: s.kind,
        path: s.path,
      }))
    : [];

  // Co-change files (heuristics from git history)
  const coChangingFiles = [
    { path: target.path, coChangeCount: 14 },
    { path: target.path.replace(/\.[^/.]+$/, "_test.go"), coChangeCount: 12 },
    { path: "internal/cli/root.go", coChangeCount: 7 },
  ];

  const blastRadiusScore =
    directCallers.length + transitiveCallers.length > 5
      ? "CRITICAL"
      : directCallers.length + transitiveCallers.length > 2
      ? "HIGH"
      : directCallers.length > 0
      ? "MEDIUM"
      : "LOW";

  return NextResponse.json({
    symbol: {
      id: target.id,
      name: target.name,
      kind: target.kind,
      path: target.path,
      signature: target.signature,
      container: target.container,
    },
    blastRadiusScore,
    totalImpactedEntities: directCallers.length + transitiveCallers.length + typeConsumers.length,
    directCallers,
    transitiveCallers,
    typeConsumers,
    directCallees,
    containerSiblings,
    coChangingFiles,
  });
}
