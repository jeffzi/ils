package cli

// SetPush makes a run `ils push` with run in place of landing.Push.
func (a *App) SetPush(run Command) {
	a.runPush = run
}
