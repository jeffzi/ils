//go:build unix

package cli

// StopIgnored is stopIgnored, for the tests of package cli_test.
func StopIgnored() bool { return stopIgnored() }

// GuardParsesFlags reports true, for the tests of package cli_test: on Unix, `ils guard` and
// `ils parse` parse their flags.
const GuardParsesFlags = true
