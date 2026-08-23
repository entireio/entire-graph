//go:build netbsd || (aix && cgo) || (solaris && cgo)

package sem

import "testing"

func TestPortableMountInventoryStartsResolver(t *testing.T) {
	resolver, err := newSameVolumePathResolver(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
}
