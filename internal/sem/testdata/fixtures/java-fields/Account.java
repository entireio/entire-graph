package bank;

public class Account {
    private String id;
    public int balance;

    public Account(String id) {
        String local = id;
        this.id = local;
    }

    public void deposit(int amount) {
        int updated = balance + amount;
        balance = updated;
    }
}
