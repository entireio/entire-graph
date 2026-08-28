import Foundation

struct Ledger {
    var total: Int = 0

    func add(_ amount: Int) -> Int {
        return total + amount
    }
}

func ledgerDouble(_ amount: Int) -> Int {
    let ledger = Ledger()
    return ledger.add(amount) * 2
}
