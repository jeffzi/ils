//go:build !unix

package cli

// onStop listens for nothing: outside Unix there is no stop signal of a terminal.
func (App) onStop(*display) {}
