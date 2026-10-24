//go:build !unix

package main

// foreground reports ils as the foreground job of every terminal: outside Unix there are no process
// groups to compare.
func foreground(uintptr) bool {
	return true
}
