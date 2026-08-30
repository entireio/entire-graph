package sem

import (
	"go/build"
	"testing"
)

func TestPathMountGuardBuildConstraints(t *testing.T) {
	targets := []struct {
		goos       string
		goarch     string
		withCgo    string
		withoutCgo string
	}{
		{goos: "aix", goarch: "ppc64", withCgo: "path_mount_guard_aix.go", withoutCgo: "path_mount_guard_unsupported.go"},
		{goos: "android", goarch: "arm64", withCgo: "path_mount_guard_linux.go", withoutCgo: "path_mount_guard_linux.go"},
		{goos: "darwin", goarch: "arm64", withCgo: "path_mount_guard_bsd.go", withoutCgo: "path_mount_guard_bsd.go"},
		{goos: "dragonfly", goarch: "amd64", withCgo: "path_mount_guard_bsd.go", withoutCgo: "path_mount_guard_bsd.go"},
		{goos: "freebsd", goarch: "amd64", withCgo: "path_mount_guard_bsd.go", withoutCgo: "path_mount_guard_bsd.go"},
		{goos: "illumos", goarch: "amd64", withCgo: "path_mount_guard_mnttab.go", withoutCgo: "path_mount_guard_unsupported.go"},
		{goos: "ios", goarch: "arm64", withCgo: "path_mount_guard_bsd.go", withoutCgo: "path_mount_guard_bsd.go"},
		{goos: "linux", goarch: "amd64", withCgo: "path_mount_guard_linux.go", withoutCgo: "path_mount_guard_linux.go"},
		{goos: "netbsd", goarch: "amd64", withCgo: "path_mount_guard_netbsd.go", withoutCgo: "path_mount_guard_netbsd.go"},
		{goos: "openbsd", goarch: "amd64", withCgo: "path_mount_guard_bsd.go", withoutCgo: "path_mount_guard_bsd.go"},
		{goos: "solaris", goarch: "amd64", withCgo: "path_mount_guard_mnttab.go", withoutCgo: "path_mount_guard_unsupported.go"},
		{goos: "windows", goarch: "amd64", withCgo: "path_mount_guard_windows.go", withoutCgo: "path_mount_guard_windows.go"},
	}
	implementations := []string{
		"path_mount_guard_aix.go",
		"path_mount_guard_bsd.go",
		"path_mount_guard_linux.go",
		"path_mount_guard_mnttab.go",
		"path_mount_guard_netbsd.go",
		"path_mount_guard_unsupported.go",
		"path_mount_guard_windows.go",
	}

	for _, target := range targets {
		for _, cgoEnabled := range []bool{false, true} {
			name := "without-cgo"
			want := target.withoutCgo
			if cgoEnabled {
				name = "with-cgo"
				want = target.withCgo
			}
			t.Run(target.goos+"/"+name, func(t *testing.T) {
				context := build.Default
				context.GOOS = target.goos
				context.GOARCH = target.goarch
				context.CgoEnabled = cgoEnabled
				var matched []string
				for _, implementation := range implementations {
					ok, err := context.MatchFile(".", implementation)
					if err != nil {
						t.Fatalf("match %s: %v", implementation, err)
					}
					if ok {
						matched = append(matched, implementation)
					}
				}
				if len(matched) != 1 || matched[0] != want {
					t.Fatalf("mount guard implementations = %v, want [%s]", matched, want)
				}
			})
		}
	}
}
