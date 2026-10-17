//go:build windows || plan9

package sem

import "os"

func openSearchVerifyRegularFile(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
