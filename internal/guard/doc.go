// Package guard reads a shell command into the invocations it holds and judges whether it may run.
//
// It runs on macOS and Linux only: on Windows only this file and quotes.go, whose quote removal
// the hook entry of the cli package reads on every system, are in the build.
package guard
