module Fixtures.Ledger where

data Ledger = Ledger { total :: Int }

add :: Ledger -> Int -> Int
add ledger amount = total ledger + amount

double :: Int -> Int
double amount = add (Ledger 0) amount * 2
