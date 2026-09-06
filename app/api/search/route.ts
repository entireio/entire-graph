import { NextRequest, NextResponse } from "next/server";
import { INITIAL_SYMBOLS, SymbolNode } from "@/lib/data";

export async function GET(req: NextRequest) {
  const searchParams = req.nextUrl.searchParams;
  const query = searchParams.get("query")?.toLowerCase() || "";
  const language = searchParams.get("language")?.toLowerCase();
  const kind = searchParams.get("kind")?.toLowerCase();
  const topK = parseInt(searchParams.get("topK") || "10", 10);

  let results: Array<SymbolNode & { score: number; matchReason: string }> = [];

  if (!query.trim()) {
    results = INITIAL_SYMBOLS.map((sym) => ({
      ...sym,
      score: 1.0,
      matchReason: "Default indexed symbol",
    }));
  } else {
    const tokens = query.split(/\s+/).filter(Boolean);

    for (const sym of INITIAL_SYMBOLS) {
      let score = 0;
      const reasons: string[] = [];

      const nameLower = sym.name.toLowerCase();
      const sigLower = sym.signature.toLowerCase();
      const docLower = (sym.docstring || "").toLowerCase();
      const codeLower = sym.codeSnippet.toLowerCase();
      const pathLower = sym.path.toLowerCase();

      // Exact name match
      if (nameLower === query) {
        score += 50;
        reasons.push("Exact symbol name match");
      } else if (nameLower.includes(query)) {
        score += 25;
        reasons.push("Symbol name substring");
      }

      // Token match across identifiers, signature, and doc
      for (const token of tokens) {
        if (nameLower.includes(token)) {
          score += 15;
        }
        if (sigLower.includes(token)) {
          score += 10;
        }
        if (docLower.includes(token)) {
          score += 8;
        }
        if (codeLower.includes(token)) {
          score += 4;
        }
        if (pathLower.includes(token)) {
          score += 5;
        }
      }

      if (score > 0) {
        results.push({
          ...sym,
          score,
          matchReason: reasons.length > 0 ? reasons.join(", ") : "Matched tokens in body and signature",
        });
      }
    }
  }

  // Filter by language if provided
  if (language && language !== "all") {
    results = results.filter((r) => r.language.toLowerCase() === language);
  }

  // Filter by kind if provided
  if (kind && kind !== "all") {
    results = results.filter((r) => r.kind.toLowerCase() === kind);
  }

  results.sort((a, b) => b.score - a.score);
  const boundedResults = results.slice(0, topK);

  return NextResponse.json({
    query,
    count: boundedResults.length,
    totalIndexed: INITIAL_SYMBOLS.length,
    results: boundedResults,
  });
}
