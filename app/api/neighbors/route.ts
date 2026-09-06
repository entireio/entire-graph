import { NextRequest, NextResponse } from "next/server";
import { INITIAL_SYMBOLS, INITIAL_EDGES } from "@/lib/data";

export async function GET(req: NextRequest) {
  const searchParams = req.nextUrl.searchParams;
  const symbol = searchParams.get("symbol") || "";
  const direction = searchParams.get("direction") || "both"; // 'in', 'out', 'both'
  const relation = searchParams.get("relation") || "all";

  // Find target symbol
  const target = INITIAL_SYMBOLS.find(
    (s) => s.name.toLowerCase() === symbol.toLowerCase() || s.id === symbol
  );

  if (!target) {
    // If not exact match, check fuzzy or return symbol list for disambiguation
    const candidates = INITIAL_SYMBOLS.filter((s) =>
      s.name.toLowerCase().includes(symbol.toLowerCase())
    );

    if (candidates.length > 0 && symbol) {
      return NextResponse.json({
        disambiguationRequired: true,
        message: "Symbol name is ambiguous or partial. Please select an exact symbol definition:",
        candidates: candidates.map((c) => ({
          id: c.id,
          name: c.name,
          path: c.path,
          line: c.startLine,
          kind: c.kind,
        })),
      });
    }

    return NextResponse.json(
      { error: `Symbol '${symbol}' not found in code graph index.` },
      { status: 404 }
    );
  }

  // Incoming callers (from -> target)
  const incoming = INITIAL_EDGES.filter((edge) => edge.toId === target.id)
    .filter((e) => relation === "all" || e.relation === relation)
    .map((e) => {
      const fromSym = INITIAL_SYMBOLS.find((s) => s.id === e.fromId);
      return {
        edgeId: e.id,
        relation: e.relation,
        confidence: e.confidence,
        callsite: e.callsite,
        caller: fromSym
          ? {
              id: fromSym.id,
              name: fromSym.name,
              path: fromSym.path,
              line: fromSym.startLine,
              kind: fromSym.kind,
            }
          : { id: e.fromId, name: e.fromName, path: "unknown", line: 0, kind: "unknown" },
      };
    });

  // Outgoing callees (target -> to)
  const outgoing = INITIAL_EDGES.filter((edge) => edge.fromId === target.id)
    .filter((e) => relation === "all" || e.relation === relation)
    .map((e) => {
      const toSym = INITIAL_SYMBOLS.find((s) => s.id === e.toId);
      return {
        edgeId: e.id,
        relation: e.relation,
        confidence: e.confidence,
        callsite: e.callsite,
        callee: toSym
          ? {
              id: toSym.id,
              name: toSym.name,
              path: toSym.path,
              line: toSym.startLine,
              kind: toSym.kind,
            }
          : { id: e.toId, name: e.toName, path: "unknown", line: 0, kind: "unknown" },
      };
    });

  return NextResponse.json({
    symbol: {
      id: target.id,
      name: target.name,
      kind: target.kind,
      path: target.path,
      startLine: target.startLine,
      endLine: target.endLine,
      signature: target.signature,
      container: target.container,
    },
    direction,
    incoming: direction === "out" ? [] : incoming,
    outgoing: direction === "in" ? [] : outgoing,
    totalIncoming: incoming.length,
    totalOutgoing: outgoing.length,
  });
}
