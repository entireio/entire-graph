export interface Ledger {
  total: number
}

export class Account {
  id: string = ""
  private balance: number = 0

  deposit(amount: number): void {
    const updated = this.balance + amount
    this.balance = updated
  }
}
