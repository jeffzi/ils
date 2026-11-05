//go:build unix

package cli

// StopIgnored is stopIgnored, for the tests of package cli_test.
func StopIgnored() bool { return stopIgnored() }
