// Package testtarget decides whether the version found of a tool suits the target a test suite
// was asked to run against.
package testtarget

import (
	"fmt"

	"github.com/jeffzi/ils/internal/child"
)

// The targets a test suite can be asked to run against. An unset target means Latest.
const (
	// Minimum asks for exactly the minimum version ils enforces.
	Minimum = "minimum"
	// Latest asks for the minimum version ils enforces or newer.
	Latest = "latest"
)

// Tool is a tool a test suite needs: its name, the environment variables that say which version
// of it the suite must find, and the minimum version ils enforces for it.
type Tool struct {
	Name string
	// TargetVar names the variable that holds the target: Minimum, Latest, or unset.
	TargetVar string
	// VersionVar names the variable that holds, when set, the exact version the suite must find.
	VersionVar string
	// MinimumVersion is the oldest version ils works with, as semver without the leading v.
	MinimumVersion string
}

// Check returns why found, a version of the tool as semver without the leading v, does not suit
// target, or nil when it does. A want that is not empty is the exact version found must be, and is
// checked before target. Minimum accepts exactly the tool's minimum; Latest and an empty target
// accept that or newer; any other target is an error.
func (t Tool) Check(target, want, found string) error {
	if want != "" && found != want {
		return fmt.Errorf("the %s found is %s, and %s wants %s", t.Name, found, t.VersionVar, want)
	}

	switch target {
	case Minimum:
		if found != t.MinimumVersion {
			return fmt.Errorf("%s is %s and the %s found is %s, want the minimum ils enforces, %s",
				t.TargetVar, target, t.Name, found, t.MinimumVersion)
		}
	case "", Latest:
		if !child.AtLeast(found, t.MinimumVersion) {
			return fmt.Errorf("the %s found is %s, older than the minimum ils enforces, %s", t.Name, found, t.MinimumVersion)
		}
	default:
		return fmt.Errorf("%s is %q, want %s, %s, or unset", t.TargetVar, target, Minimum, Latest)
	}

	return nil
}

// CheckOutput returns why a tool whose `<name> --version` prints out does not suit target, or nil
// when it does. Only the child.VersionLine of out is read: versionOf must read a version from it,
// and that version must suit target and want, as Check decides. A line that names no version
// versionOf reads is an error.
func (t Tool) CheckOutput(target, want, out string, versionOf func(line string) string) error {
	line := child.VersionLine(out)

	found := versionOf(line)
	if found == "" {
		return fmt.Errorf("%s --version printed %q, which names no version the suite can read", t.Name, line)
	}

	return t.Check(target, want, found)
}
