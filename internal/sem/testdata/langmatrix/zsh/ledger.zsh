#!/usr/bin/env zsh

ledger_add() {
  local amount="$1"
  print $(( amount + 1 ))
}

ledger_double() {
  local amount="$1"
  ledger_add "$amount"
}

ledger_double 2
