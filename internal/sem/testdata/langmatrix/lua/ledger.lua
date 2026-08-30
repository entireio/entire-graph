local Ledger = {}
Ledger.__index = Ledger

function Ledger.new()
  return setmetatable({ total = 0 }, Ledger)
end

function Ledger.add(self, amount)
  return self.total + amount
end

local function ledger_double(amount)
  local ledger = Ledger.new()
  return Ledger.add(ledger, amount) * 2
end

return ledger_double
