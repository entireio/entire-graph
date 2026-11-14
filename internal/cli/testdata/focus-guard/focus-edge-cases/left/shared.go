package left

// Shared has a namesake in right/shared.go, so --file chooses between them.
func Shared() string {
	return "left"
}

func UseShared() string {
	return Shared()
}
