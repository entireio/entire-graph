class Ledger {
    int total = 0

    int add(int amount) {
        return total + amount
    }
}

int ledgerDouble(int amount) {
    def ledger = new Ledger()
    return ledger.add(amount) * 2
}
