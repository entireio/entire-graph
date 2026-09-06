import { NextResponse } from "next/server";
import { MOCK_STATS } from "@/lib/data";

export async function GET() {
  return NextResponse.json({
    metrics: MOCK_STATS,
    savingsModel: "Each search/neighbors/impact query credits the whole-file read it replaces: on-disk size of the top-hit file minus returned payload bytes (4 bytes = 1 token).",
    timestamp: new Date().toISOString(),
  });
}
