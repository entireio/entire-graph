class Ledger:
    def __init__(self) -> None:
        self.total = 0

    def add(self, amount: int) -> int:
        return self.total + amount


def ledger_double(amount: int) -> int:
    ledger = Ledger()
    return ledger.add(amount) * 2
