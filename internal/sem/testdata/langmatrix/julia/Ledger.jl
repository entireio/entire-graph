module Ledgers

struct Ledger
    total::Int
end

function add(ledger::Ledger, amount::Int)
    return ledger.total + amount
end

function double(amount::Int)
    return add(Ledger(0), amount) * 2
end

end
