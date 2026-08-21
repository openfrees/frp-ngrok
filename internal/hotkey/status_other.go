//go:build !darwin || !cgo

package hotkey

func openRunStatus(name, command string) int     { return 0 }
func appendRunStatus(id int, text string)        {}
func replaceRunStatusOutput(id int, text string) {}
func replaceRunStatusCountdown(id int, text string) {
}
func finishRunStatus(id int, text string, ok bool) {
	flushRunStatusFeed(id)
	closeRunStatusFeed(id)
}
func closeRunStatusAfterCountdown(id int) {}
