package bank

// Account has fields that must be emitted as field symbols under Account.
type Account struct {
	ID      string
	Balance int
	owner   string
}

// Open is a function whose parameters and locals must NOT become fields.
func Open(name string, initial int) Account {
	normalized := name
	total := initial
	return Account{ID: normalized, Balance: total}
}

// Deposit is a method whose local variables must NOT become fields.
func (a *Account) Deposit(amount int) {
	updated := a.Balance + amount
	a.Balance = updated
}
