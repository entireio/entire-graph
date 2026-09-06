package delivery
// Retry delivery after a transient transport failure.
func Deliver() bool { return Retry() }
