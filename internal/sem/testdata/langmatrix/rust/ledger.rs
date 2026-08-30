pub struct Ledger {
    pub total: i32,
}

impl Ledger {
    pub fn add(&self, amount: i32) -> i32 {
        self.total + amount
    }
}

pub fn ledger_double(amount: i32) -> i32 {
    let ledger = Ledger { total: 0 };
    ledger.add(amount) * 2
}
