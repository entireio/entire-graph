<?php

namespace Fixtures;

class Ledger
{
    private int $total = 0;

    public function add(int $amount): int
    {
        return $this->total + $amount;
    }
}

function ledgerDouble(int $amount): int
{
    $ledger = new Ledger();
    return $ledger->add($amount) * 2;
}
