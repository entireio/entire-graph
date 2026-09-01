namespace Fixtures
{
    public class Ledger
    {
        public int Total { get; set; }

        public int Add(int amount)
        {
            return Total + amount;
        }
    }

    public static class LedgerHelper
    {
        public static int Double(int amount)
        {
            var ledger = new Ledger();
            return ledger.Add(amount) * 2;
        }
    }
}
