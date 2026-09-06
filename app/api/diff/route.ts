import { NextRequest, NextResponse } from "next/server";
import { RECENT_DIFFS } from "@/lib/data";

export async function GET(req: NextRequest) {
  const searchParams = req.nextUrl.searchParams;
  const filterRisk = searchParams.get("risk");

  let diffs = [...RECENT_DIFFS];
  if (filterRisk && filterRisk !== "all") {
    diffs = diffs.filter((d) => d.riskLevel.toLowerCase() === filterRisk.toLowerCase());
  }

  return NextResponse.json({
    baseRef: "origin/main",
    headRef: "HEAD",
    totalChanges: diffs.length,
    highRiskChanges: diffs.filter((d) => d.riskLevel === "HIGH" || d.riskLevel === "CRITICAL").length,
    diffs,
  });
}
