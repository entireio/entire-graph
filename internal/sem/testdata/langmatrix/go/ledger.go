package fixtures

type Ledger struct {
	Total int
}

func (l Ledger) Add(amount int) int {
	return l.Total + amount
}

func LedgerDouble(amount int) int {
	ledger := Ledger{}
	return ledger.Add(amount) * 2
}
