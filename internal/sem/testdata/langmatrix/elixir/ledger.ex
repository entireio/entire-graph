defmodule Fixtures.Ledger do
  defstruct total: 0

  def add(ledger, amount) do
    ledger.total + amount
  end

  def double(amount) do
    add(%Fixtures.Ledger{}, amount) * 2
  end
end
