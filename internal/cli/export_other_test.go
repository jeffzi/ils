//go:build !unix

package cli

// StopIgnored reports false, for the tests of package cli_test: outside Unix there is no stop
// signal of a terminal to ignore.
func StopIgnored() bool { return false }
