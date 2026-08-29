package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultPort is where `redline open` and `redline serve` put the report.
// A loopback HTTP URL works in editor webviews; file:// usually does not.
const DefaultPort = 8765

// portSpan is how far past DefaultPort openDir will look for a free port when
// something else owns the default. A fixed port cannot be assumed: the
// reviewer may have two repositories open, and serving the wrong repository's
// report is worse than serving it somewhere unexpected.
const portSpan = 20

// idPath answers with the absolute evidence directory this server was started
// for. It is how a later run tells "our server" from "something on our port".
const idPath = "/.redline-id"

// idleTimeout shuts a detached server down once nobody is reading it, so a
// review does not leave the change's diffs served until the machine reboots.
const idleTimeout = 30 * time.Minute

// pidFile records the running server inside the evidence directory so a later
// run — or `redline serve --stop` — can find and stop it.
const pidFile = "serve.json"

// serverInfo is both the pidFile payload and the idPath response body.
type serverInfo struct {
	PID  int    `json:"pid"`
	Port int    `json:"port"`
	Dir  string `json:"dir"`
}

// Serve blocks, serving outDir on 127.0.0.1:port until it goes idle.
func Serve(outDir string, port int) error {
	if port <= 0 {
		port = DefaultPort
	}
	abs, err := evidenceDir(outDir)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("port %d: %w", port, err)
	}

	idle := newIdleTimer(idleTimeout)
	mux := http.NewServeMux()
	files := http.FileServer(http.Dir(abs))
	info := serverInfo{PID: os.Getpid(), Port: port, Dir: abs}
	mux.HandleFunc(idPath, func(w http.ResponseWriter, r *http.Request) {
		idle.touch()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info)
	})
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idle.touch()
		files.ServeHTTP(w, r)
	}))

	srv := &http.Server{Handler: mux}
	writePID(abs, info)
	defer clearPID(abs, info)

	go func() {
		idle.wait()
		_ = srv.Close()
	}()

	fmt.Fprintf(os.Stderr, "Report: %s\n", ReportURL(port))
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop shuts down the server recorded for outDir. A missing or stale record
// is not an error: the goal state is "nothing serving this directory".
func Stop(outDir string) error {
	abs, err := filepath.Abs(outDir)
	if err != nil {
		return err
	}
	info, ok := readPID(abs)
	if !ok {
		return nil
	}
	// Only signal a process that still answers as this directory's server.
	// A PID from a record left behind by a crash may since have been reused
	// by something unrelated, and Redline must not kill it.
	if info.PID == os.Getpid() || !servesDir(info.Port, abs) {
		_ = os.Remove(filepath.Join(abs, pidFile))
		return nil
	}
	proc, err := os.FindProcess(info.PID)
	if err == nil {
		_ = proc.Signal(os.Interrupt)
	}
	_ = os.Remove(filepath.Join(abs, pidFile))
	fmt.Fprintf(os.Stderr, "Stopped the report server on port %d.\n", info.Port)
	return nil
}

// idleTimer closes the server after d with no requests.
type idleTimer struct {
	mu   sync.Mutex
	last time.Time
	d    time.Duration
}

func newIdleTimer(d time.Duration) *idleTimer {
	return &idleTimer{last: time.Now(), d: d}
}

func (t *idleTimer) touch() {
	t.mu.Lock()
	t.last = time.Now()
	t.mu.Unlock()
}

func (t *idleTimer) wait() {
	for {
		t.mu.Lock()
		remaining := t.d - time.Since(t.last)
		t.mu.Unlock()
		if remaining <= 0 {
			return
		}
		time.Sleep(remaining)
	}
}

func writePID(abs string, info serverInfo) {
	buf, err := json.Marshal(info)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(abs, pidFile), append(buf, '\n'), 0o644)
}

// clearPID removes the record only if it is still ours, so a server that
// replaced us keeps its own.
func clearPID(abs string, info serverInfo) {
	if got, ok := readPID(abs); ok && got.PID != info.PID {
		return
	}
	_ = os.Remove(filepath.Join(abs, pidFile))
}

func readPID(abs string) (serverInfo, bool) {
	buf, err := os.ReadFile(filepath.Join(abs, pidFile))
	if err != nil {
		return serverInfo{}, false
	}
	var info serverInfo
	if err := json.Unmarshal(buf, &info); err != nil || info.PID <= 0 {
		return serverInfo{}, false
	}
	return info, true
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
	abs, err := evidenceDir(outDir)
	if err != nil {
		return "", err
	}
	// Reuse a server only when it identifies itself as serving *this*
	// evidence directory. A bare 200 on the port proves nothing: it may be
	// another repository's report, or an unrelated static server.
	found, ok := findServer(abs, port)
	if !ok {
		found, err = spawnServer(abs, port)
		if err != nil {
			return "", err
		}
	}
	url := ReportURL(found)
	if browse {
		if err := openBrowser(url); err != nil {
			return url, fmt.Errorf("report is at %s (could not open a browser: %w)", url, err)
		}
	}
	return url, nil
}

// findServer looks for a running Redline server for abs, starting at port.
func findServer(abs string, port int) (int, bool) {
	if info, ok := readPID(abs); ok && sameDir(info.Dir, abs) && servesDir(info.Port, abs) {
		return info.Port, true
	}
	for p := port; p < port+portSpan; p++ {
		if servesDir(p, abs) {
			return p, true
		}
	}
	return 0, false
}

// spawnServer starts a detached server on the first port from `port` that is
// free, and waits for it to answer for abs.
func spawnServer(abs string, port int) (int, error) {
	free, err := freePort(port)
	if err != nil {
		return 0, err
	}
	if err := startServe(abs, free); err != nil {
		return 0, err
	}
	if err := waitServing(free, abs, 5*time.Second); err != nil {
		return 0, err
	}
	return free, nil
}

func freePort(port int) (int, error) {
	for p := port; p < port+portSpan; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return p, nil
	}
	return 0, fmt.Errorf("no free loopback port in %d-%d for the report server", port, port+portSpan-1)
}

// servesDir reports whether a Redline server for abs answers on port.
func servesDir(port int, abs string) bool {
	c := &http.Client{Timeout: 400 * time.Millisecond}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, idPath))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return false
	}
	var info serverInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return false
	}
	return sameDir(info.Dir, abs)
}

func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.TrimRight(filepath.Clean(a), string(filepath.Separator)) ==
		strings.TrimRight(filepath.Clean(b), string(filepath.Separator))
}

func waitServing(port int, abs string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if servesDir(port, abs) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("report server did not become ready at %s", ReportURL(port))
}

func evidenceDir(outDir string) (string, error) {
	abs, err := filepath.Abs(outDir)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(abs, "report.html")); err != nil {
		return "", fmt.Errorf("no report in %s (run `redline review` first): %w", abs, err)
	}
	return abs, nil
}

func startServe(outDir string, port int) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "serve", "--out", outDir, "--port", fmt.Sprintf("%d", port))
	cmd.Dir = filepath.Dir(outDir)
	detach(cmd)
	return cmd.Start()
}
