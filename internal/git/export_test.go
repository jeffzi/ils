package git

// LineWords is lineWords, for the tests of package git_test.
func LineWords(line string) ([]string, bool) { return lineWords(line) }

// ProgramWord is programWord, for the tests of package git_test.
func ProgramWord(line string) (string, bool) { return programWord(line) }

// SplitPushReport is splitPushReport without the status it reads, for the tests of package
// git_test.
func SplitPushReport(stdout, spec string) (hook string, reported bool) {
	hook, _, reported = splitPushReport(stdout, spec)

	return hook, reported
}
