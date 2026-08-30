export class Ledger {
  total = 0;

  add(amount: number): number {
    return this.total + amount;
  }
}

export function ledgerDouble(amount: number): number {
  const ledger = new Ledger();
  return ledger.add(amount) * 2;
}
