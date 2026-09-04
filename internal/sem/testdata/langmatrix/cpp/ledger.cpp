#include <string>

class Ledger {
public:
    int Add(int amount) const;

private:
    int total_ = 0;
};

int Ledger::Add(int amount) const {
    return total_ + amount;
}

int LedgerDouble(int amount) {
    Ledger ledger;
    return ledger.Add(amount) * 2;
}
