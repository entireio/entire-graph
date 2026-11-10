export class Ledger {
  constructor() {
    this.total = 0;
  }

  add(amount) {
    return this.total + amount;
  }
}

export function ledgerDouble(amount) {
  const ledger = new Ledger();
  return ledger.add(amount) * 2;
}
