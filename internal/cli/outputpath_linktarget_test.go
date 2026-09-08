package cli

import (
	"io/fs"
	"path/filepath"
	"runtime"
	"testing"
)

// linkTargetAnchorNames renders the classification in a failure message, because the numbers an
// untyped iota comparison prints say nothing about which anchor was meant.
var linkTargetAnchorNames = map[linkTargetAnchor]string{
	linkTargetRelative:      "relative (resolved against the link's parent)",
	linkTargetQualified:     "fully qualified (restarts at the volume the target names)",
	linkTargetVolumeRooted:  "drive-rooted (restarts at the volume being walked)",
	linkTargetDriveRelative: "drive-relative (no anchor a pinned walk can open)",
}

// TestLinkTargetAnchorPinsEveryWindowsRootedSpelling is the regression for the rooted-target
// misclassification in the unconfined output walk.
//
// The walk restarts at a volume root when a symbolic link's target is anchored there, and it used
// to decide that with filepath.IsAbs alone. Windows has FOUR spellings that are rooted in different
// senses and IsAbs separates them wrongly:
//
//	C:\dir\file            fully qualified   IsAbs true    restart at C:\
//	\\server\share\file    fully qualified   IsAbs true    restart at \\server\share\
//	\\?\C:\dir\file        fully qualified   IsAbs true    restart at \\?\C:\
//	\dir\file              ROOTED            IsAbs FALSE   restart at the walked volume
//	C:file                 neither           IsAbs FALSE   no anchor at all
//
// `\dir\file` is the defect. IsAbs reports false for it, so the components were appended to the
// link's PARENT instead of to a volume root: an unconfined `--report` or `--record-baseline` routed
// through such a link wrote to a file the caller never named. `C:file` is the shape that must NOT
// restart — it is resolved against that drive's own working directory, which is process-global
// state this walk has no handle on — and it is refused instead.
//
// Windows cannot execute on this repository's development hosts, so the assertion is on the pure
// decision function with the host family passed in. The syscall path that consumes the decision —
// the restart itself, and os.Root's behaviour on each volume spelling — is covered only by the
// Windows CI leg.
func TestLinkTargetAnchorPinsEveryWindowsRootedSpelling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		goos   string
		target string
		want   linkTargetAnchor
		why    string
	}{
		// The four Windows rooted shapes.
		{"windows", `C:\other\out.json`, linkTargetQualified,
			"a drive-absolute target names its own volume"},
		{"windows", `c:/other/out.json`, linkTargetQualified,
			"Windows accepts the forward-slash separator and a lower-case drive letter"},
		{"windows", `\\server\share\out.json`, linkTargetQualified,
			"a UNC target carries \\\\server\\share as its volume"},
		{"windows", `\\?\C:\other\out.json`, linkTargetQualified,
			"an extended-length target carries \\\\?\\C: as its volume"},
		{"windows", `\\.\C:\other\out.json`, linkTargetQualified,
			"a device-namespace target is rooted the same way"},
		{"windows", `\other\out.json`, linkTargetVolumeRooted,
			"THE DEFECT: rooted at the walked volume, and filepath.IsAbs reports false for it"},
		{"windows", `/other/out.json`, linkTargetVolumeRooted,
			"the forward-slash spelling of the same drive-rooted target"},
		{"windows", `\`, linkTargetVolumeRooted,
			"a bare separator is the volume root itself"},

		// The shape that is rooted in NO sense and must not restart.
		{"windows", `C:other\out.json`, linkTargetDriveRelative,
			"drive-relative: resolved against C:'s own working directory"},
		{"windows", `C:`, linkTargetDriveRelative,
			"a bare drive names that drive's working directory"},

		// Ordinary Windows relative targets.
		{"windows", `other\out.json`, linkTargetRelative, "a plain relative target"},
		{"windows", `..\out.json`, linkTargetRelative, "a relative target that leaves the parent"},
		{"windows", `C.\out.json`, linkTargetRelative, "a leading letter without a colon is a name"},
		{"windows", `1:\out.json`, linkTargetRelative, "only a letter can name a drive"},
		{"windows", "", linkTargetRelative, "an empty target has no anchor to restart at"},

		// POSIX families: exactly one rooted spelling, and the Windows ones are ordinary
		// filenames there. Classifying `C:x` or `\x` as anything but relative on POSIX would
		// refuse a legal filename.
		{"linux", "/other/out.json", linkTargetQualified, "the only POSIX rooted spelling"},
		{"linux", "//other/out.json", linkTargetQualified, "two leading slashes are still rooted"},
		{"linux", "other/out.json", linkTargetRelative, "a plain relative target"},
		{"linux", `C:\other\out.json`, linkTargetRelative, "a legal POSIX filename, not a drive"},
		{"linux", `\other\out.json`, linkTargetRelative, "a legal POSIX filename, not a root"},
		{"linux", "", linkTargetRelative, "an empty target has no anchor to restart at"},
		{"darwin", "/other/out.json", linkTargetQualified, "the only POSIX rooted spelling"},
		{"darwin", `C:other`, linkTargetRelative, "a legal POSIX filename, not a drive"},
	}
	for _, test := range tests {
		t.Run(test.goos+" "+test.target, func(t *testing.T) {
			t.Parallel()
			got := linkTargetAnchorFor(test.goos, test.target)
			if got != test.want {
				t.Fatalf("linkTargetAnchorFor(%q, %q) = %s, want %s: %s",
					test.goos, test.target,
					linkTargetAnchorNames[got], linkTargetAnchorNames[test.want], test.why)
			}
		})
	}
}

// TestLinkTargetAnchorAgreesWithFilepathOnThisHost keeps the hand-written Windows rules honest on
// the family that CAN be executed here.
//
// The decision function spells out path syntax rather than asking filepath, so nothing but a test
// stops it from drifting away from what the standard library — and therefore the kernel this walk
// hands its components to — actually does. On a POSIX host every classification other than relative
// must coincide exactly with filepath.IsAbs; the Windows-only anchors must never appear at all,
// since a target the host reads as an ordinary filename has to stay relative.
func TestLinkTargetAnchorAgreesWithFilepathOnThisHost(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the Windows anchors are pinned by the table above; this checks the POSIX host")
	}
	targets := []string{
		"/other/out.json", "//other/out.json", "other/out.json", "../out.json", "",
		`C:\other\out.json`, `C:other`, `\other\out.json`, `\\server\share\out.json`,
	}
	for _, target := range targets {
		got := linkTargetAnchorOf(target)
		switch got {
		case linkTargetVolumeRooted, linkTargetDriveRelative:
			t.Fatalf("linkTargetAnchorOf(%q) = %s on %s, where no such spelling exists",
				target, linkTargetAnchorNames[got], runtime.GOOS)
		}
		if want := filepath.IsAbs(target); (got == linkTargetQualified) != want {
			t.Fatalf("linkTargetAnchorOf(%q) = %s but filepath.IsAbs = %v",
				target, linkTargetAnchorNames[got], want)
		}
	}
}

// TestUnconfinedRouteClassifiesWindowsNameSurrogates is the regression for the unconfined output
// walk deciding what a component IS from its mode alone.
//
// Since Go 1.23, Windows reports every reparse point Go does not itself decode as ModeIrregular.
// The two it does decode — IO_REPARSE_TAG_SYMLINK and IO_REPARSE_TAG_MOUNT_POINT — still arrive as
// ModeSymlink; every other tag does not, and that remainder contains NAME SURROGATES the kernel
// follows during path resolution: a WSL LX symlink, a junction whose tag this build cannot decode,
// IO_REPARSE_TAG_GLOBAL_REPARSE. A ModeSymlink-only test read each of those as an ordinary
// component, so the walk opened the ALIAS and then compared the object it received against the name
// it had asked for — a comparison a redirect cannot survive — and refused a caller-owned `--report`
// or `--record-baseline` path Windows resolves perfectly well.
//
// The predicate says "classify this", not "this is an alias": the tags that merely ANNOTATE a file
// are ModeIrregular too, and the raw reparse tag is what separates them. The limit is the same one
// stated at the top of this file — this proves the decision, not the kernel behaviour it models.
func TestUnconfinedRouteClassifiesWindowsNameSurrogates(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name string
		goos string
		mode fs.FileMode
		want bool
	}{
		{
			name: "a Windows reparse point Go cannot decode is not an ordinary directory",
			goos: "windows", mode: fs.ModeDir | fs.ModeIrregular, want: true,
		},
		{
			name: "nor an ordinary file",
			goos: "windows", mode: fs.ModeIrregular, want: true,
		},
		{
			name: "a Windows symlink or junction is still one",
			goos: "windows", mode: fs.ModeSymlink, want: true,
		},
		{
			name: "an ordinary Windows directory is opened as one",
			goos: "windows", mode: fs.ModeDir, want: false,
		},
		{
			name: "a POSIX symlink is an alias",
			goos: "linux", mode: fs.ModeSymlink, want: true,
		},
		{
			name: "POSIX has no reparse points, so ModeIrregular is not an alias there",
			goos: "linux", mode: fs.ModeIrregular, want: false,
		},
		{
			name: "an ordinary POSIX file is opened as one",
			goos: "darwin", mode: 0, want: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := unconfinedRouteAliasCandidate(testCase.goos, testCase.mode); got != testCase.want {
				t.Fatalf("unconfinedRouteAliasCandidate(%q, %v) = %v, want %v",
					testCase.goos, testCase.mode, got, testCase.want)
			}
		})
	}
}
