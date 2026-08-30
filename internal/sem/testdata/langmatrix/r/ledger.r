make_ledger <- function() {
  list(total = 0)
}

ledger_add <- function(ledger, amount) {
  ledger$total + amount
}

ledger_double <- function(amount) {
  ledger_add(make_ledger(), amount) * 2
}
