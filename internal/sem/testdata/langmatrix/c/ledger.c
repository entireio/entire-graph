#include <stddef.h>

struct Ledger {
    int total;
};

int ledger_add(struct Ledger *ledger, int amount) {
    return ledger->total + amount;
}

int ledger_double(int amount) {
    struct Ledger ledger = {0};
    return ledger_add(&ledger, amount) * 2;
}
