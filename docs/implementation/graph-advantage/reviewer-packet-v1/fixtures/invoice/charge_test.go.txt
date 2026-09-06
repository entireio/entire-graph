package invoice
import "testing"
func TestChargeInvoice(t *testing.T) { if ChargeInvoice()!=7 { t.Fatal("bad charge") } }
