// Package gittest builds real git repositories for tests: a working repository with a bare origin
// under the test's temporary directory, isolated from the user's git configuration.
//
// It also provides a fake git: FakeGit puts the test binary first on PATH under git's name, and a
// package whose tests use it calls Main from TestMain before running any test. Spawner makes the
// test binary, through the same Main, a program that exits while a process it started still holds
// its output.
//
// InstallFakeGit instead builds the fake git as a program of its own, once, from a package's
// TestMain, and puts it first on PATH: a test then fakes git by appending Fake.Settings to a git
// runner's environment, without touching the process environment.
package gittest
