#!/usr/bin/env bash

ledger_add() {
  local amount="$1"
  echo $(( amount + 1 ))
}

ledger_double() {
  local amount="$1"
  ledger_add "$amount"
}

ledger_double 2
