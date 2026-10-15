package git

// VersionOf exposes versionOf to the guard that checks the git the tests run.
func VersionOf(line string) string { return versionOf(line) }
