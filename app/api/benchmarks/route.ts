import { NextResponse } from "next/server";
import { BENCHMARKS_DATA } from "@/lib/data";

export async function GET() {
  return NextResponse.json({
    benchmark: "LoCoMo 1,540 Shared-Reader Code Retrieval Benchmark",
    dateMeasured: "2026-08-14",
    systems: BENCHMARKS_DATA,
  });
}
