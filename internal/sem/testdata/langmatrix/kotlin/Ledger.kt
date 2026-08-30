package fixtures

class Ledger(var total: Int = 0) {
    fun add(amount: Int): Int {
        return total + amount
    }
}

fun ledgerDouble(amount: Int): Int {
    val ledger = Ledger()
    return ledger.add(amount) * 2
}
