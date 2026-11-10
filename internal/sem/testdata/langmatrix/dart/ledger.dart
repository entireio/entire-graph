class Ledger {
  int total = 0;

  int add(int amount) {
    return total + amount;
  }
}

int ledgerDouble(int amount) {
  final ledger = Ledger();
  return ledger.add(amount) * 2;
}
