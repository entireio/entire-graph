struct Ledger {
    var balance: Int
}

func settle(ledger: Ledger) -> Ledger {
    return post(ledger: ledger)
}

func post(ledger: Ledger) -> Ledger {
    return ledger
}

class Account {
    // A `func` declared inside a method body is a LOCAL FUNCTION, visible only
    // inside apply(). It takes no `self` and nothing can reach it as
    // `Account.adjust`, so it must emit as the function `Account.apply.adjust`
    // — emitting the method `Account.adjust` invents a member and pushes the
    // real `adjust` below onto a `#sig:`-disambiguated ID (issue #259).
    func apply(_ amount: Int) -> Int {
        func adjust(_ value: Int) -> Int {
            return value + 1
        }

        return adjust(amount)
    }

    func adjust(_ amount: Int) -> Int {
        return amount - 1
    }
}
