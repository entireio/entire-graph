package edge

// dıal holds a dotless ı (U+0131). strings.ToUpper maps it to DIAL, which
// strings.EqualFold does not fold back to dıal, so DIAL is no exact name for it.
func dıal() string {
	return "dial"
}
