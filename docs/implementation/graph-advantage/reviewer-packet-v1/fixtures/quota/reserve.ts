import { validateQuota } from "./validate";
export function reserveQuota(amount: number) { return validateQuota(amount); }
