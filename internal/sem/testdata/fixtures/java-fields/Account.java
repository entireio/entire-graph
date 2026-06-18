package bank;

public class Account {
    private String id;
    public int balance;

    public void deposit(int amount) {
        int updated = this.balance + amount;
        this.balance = updated;
    }

    public String identify(String prefix) {
        String local = prefix;
        return local + this.id;
    }
}
