struct Ledger {
    var balance: Int
}

func settle(ledger: Ledger) -> Ledger {
    return post(ledger: ledger)
}

func post(ledger: Ledger) -> Ledger {
    return ledger
}
