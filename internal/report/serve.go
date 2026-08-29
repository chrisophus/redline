package report

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// DefaultPort is where `redline open` and `redline serve` put the report.
// A loopback HTTP URL works in editor webviews; file:// usually does not.
const DefaultPort = 8765

// Serve blocks, serving outDir on 127.0.0.1:port.
func Serve(outDir string, port int) error {
	if port <= 0 {
		port = DefaultPort
	}
	abs, err := filepath.Abs(outDir)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(abs, "report.html")); err != nil {
		return fmt.Errorf("no report in %s (run `redline review` first): %w", abs, err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	fmt.Fprintf(os.Stderr, "Report: %s\n", ReportURL(port))
	return http.ListenAndServe(addr, http.FileServer(http.Dir(abs)))
}

// ReportURL is the loopback address of report.html on port.
func ReportURL(port int) string {
	if port <= 0 {
		port = DefaultPort
	}
	return fmt.Sprintf("http://127.0.0.1:%d/report.html", port)
}

// OpenDir makes the report reachable over HTTP and, if browse is true, opens
// it in the user's browser. The returned URL is always meant to be shown:
// a review nobody can click did not happen.
func OpenDir(outDir string, browse bool) (string, error) {
	return OpenOn(outDir, browse, DefaultPort)
}

// OpenOn is OpenDir on an explicit port.
func OpenOn(outDir string, browse bool, port int) (string, error) {
	if port <= 0 {
		port = DefaultPort
	}
	return openDir(outDir, browse, port)
}

func openDir(outDir string, browse bool, port int) (string, error) {
	abs, err := filepath.Abs(outDir)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(abs, "report.html")); err != nil {
		return "", fmt.Errorf("no report in %s (run `redline review` first): %w", abs, err)
	}
	url := ReportURL(port)
	if !ready(url) {
		if err := startServe(abs, port); err != nil {
			return url, err
		}
		if err := waitReady(url, 3*time.Second); err != nil {
			return url, err
		}
	}
	if browse {
		if err := openBrowser(url); err != nil {
			return url, fmt.Errorf("report is at %s (could not open a browser: %w)", url, err)
		}
	}
	return url, nil
}

func ready(url string) bool {
	c := &http.Client{Timeout: 400 * time.Millisecond}
	resp, err := c.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func waitReady(url string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ready(url) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("report server did not become ready at %s", url)
}

func startServe(outDir string, port int) error {
	// If something else is already bound, waitReady will fail unless it
	// happens to serve our report — try a listen first so we get a clear error.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		if ready(ReportURL(port)) {
			return nil
		}
		return fmt.Errorf("port %d is busy and is not serving the report", port)
	}
	_ = ln.Close()

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "serve", "--out", outDir, "--port", fmt.Sprintf("%d", port))
	cmd.Dir = filepath.Dir(outDir)
	detach(cmd)
	return cmd.Start()
}
