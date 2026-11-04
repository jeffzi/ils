//go:build unix

package guard

// isASCIILetter reports whether c is an ASCII letter, in either case.
func isASCIILetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
