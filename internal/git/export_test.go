package git

// FetchedRefs is fetchedRefs, for the tests of package git_test.
const FetchedRefs = fetchedRefs

// ProgramWord is programWord, for the tests of package git_test.
func ProgramWord(line string) (string, bool) { return programWord(line) }

// SplitPushReport is the hook's part of splitPushReport, for the tests of package git_test.
func SplitPushReport(stdout, spec string) string {
	hook, _ := splitPushReport(stdout, spec)

	return hook
}
