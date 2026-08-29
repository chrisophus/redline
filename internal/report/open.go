package report

import (
	"os"
	"os/exec"
	"runtime"
)

// Open shows the report in the user's browser. Prefer OpenDir: file://
// paths are rejected by editor webviews.
func Open(path string) error {
	return openBrowser(path)
}

func openBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Start()
}
