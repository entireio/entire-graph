class Ledger
  attr_accessor :total

  def initialize
    @total = 0
  end

  def add(amount)
    @total + amount
  end
end

def ledger_double(amount)
  ledger = Ledger.new
  ledger.add(amount) * 2
end
