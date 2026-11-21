package edge

// KelvinTemp starts with the Kelvin sign (U+212A), which folds to k but is
// not ASCII, so a lookup of kelvintemp matches it only through a full
// Unicode case fold.
func KelvinTemp() string {
	return "kelvin"
}
