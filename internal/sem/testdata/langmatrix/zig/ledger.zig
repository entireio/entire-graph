const std = @import("std");

pub const Ledger = struct {
    total: i32,

    pub fn add(self: Ledger, amount: i32) i32 {
        return self.total + amount;
    }
};

pub fn ledgerDouble(amount: i32) i32 {
    const ledger = Ledger{ .total = 0 };
    return ledger.add(amount) * 2;
}
