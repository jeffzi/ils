// Package gittest builds real git repositories for tests: a working repository with a bare origin
// under the test's temporary directory, isolated from the user's git configuration.
//
// It also provides a fake git: FakeGit puts the test binary first on PATH under git's name, and a
// package whose tests use it calls Main from TestMain before running any test.
package gittest
