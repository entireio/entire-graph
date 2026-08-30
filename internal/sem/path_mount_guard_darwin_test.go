//go:build darwin

package sem

import (
	"os"
	"testing"
)

func TestDarwinResolverAllowsExplicitSameDeviceMount(t *testing.T) {
	const dataVolume = "/System/Volumes/Data"
	rootInfo, rootErr := os.Stat("/")
	dataInfo, dataErr := os.Stat(dataVolume)
	if rootErr != nil || dataErr != nil {
		t.Skip("macOS data volume is unavailable")
	}
	rootDevice, rootOK := fileSystemDevice(rootInfo)
	dataDevice, dataOK := fileSystemDevice(dataInfo)
	if !rootOK || !dataOK || rootDevice != dataDevice {
		t.Skip("macOS data volume is not a same-device mount on this host")
	}

	resolver, err := newSameVolumePathResolver(dataVolume)
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	opened, _, err := resolver.open(dataVolume)
	if err != nil {
		t.Fatalf("explicitly selected same-device mount was rejected: %v", err)
	}
	_ = opened.Close()
}
