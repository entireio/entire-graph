package fixtures;

public class Ledger {
    private int total;

    public int add(int amount) {
        return total + amount;
    }

    public static int ledgerDouble(int amount) {
        Ledger ledger = new Ledger();
        return ledger.add(amount) * 2;
    }
}
