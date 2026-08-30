module Fixtures.Ledger

type Ledger = { Total: int }

let add (ledger: Ledger) (amount: int) : int =
    ledger.Total + amount

let double (amount: int) : int =
    add { Total = 0 } amount * 2
