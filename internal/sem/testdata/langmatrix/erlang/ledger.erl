-module(ledger).
-export([add/2, double/1]).

-record(ledger, {total = 0}).

add(Ledger, Amount) ->
    Ledger#ledger.total + Amount.

double(Amount) ->
    add(#ledger{}, Amount) * 2.
