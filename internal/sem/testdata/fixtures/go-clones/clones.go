package clones

import "strings"

// sumPositive and tallyPositive are copy-paste duplicates (identical body,
// different name) and should be linked by SIMILAR_TO.
func sumPositive(values []int) int {
	total := 0
	for _, value := range values {
		if value > 0 {
			total += value
		}
	}
	return total
}

func tallyPositive(values []int) int {
	total := 0
	for _, value := range values {
		if value > 0 {
			total += value
		}
	}
	return total
}

// distinct is unrelated and must not be linked.
func distinct(name string) string {
	return strings.ToUpper(strings.TrimSpace(name))
}
