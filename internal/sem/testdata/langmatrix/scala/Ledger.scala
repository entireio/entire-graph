package fixtures

class Ledger(var total: Int = 0) {
  def add(amount: Int): Int = total + amount
}

object LedgerApp {
  def ledgerDouble(amount: Int): Int = {
    val ledger = new Ledger()
    ledger.add(amount) * 2
  }
}
