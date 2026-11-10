type ledger = { total : int }

let add ledger amount = ledger.total + amount

let double amount = add { total = 0 } amount * 2

module Ledgers = struct
  let empty = { total = 0 }
end
