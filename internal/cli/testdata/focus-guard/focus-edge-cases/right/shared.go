package right

// Shared has a namesake in left/shared.go, so --file chooses between them.
func Shared() string {
	return "right"
}

func CallShared() string {
	return Shared()
}
