namespace Bank
{
    public class Account
    {
        public string Id;
        private int balance;
        public int Balance { get; set; }

        public void Deposit(int amount)
        {
            int updated = balance + amount;
            balance = updated;
        }
    }
}
