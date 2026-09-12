package report

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServeDeliversReportHTML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.html"), []byte("<html>Redline</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go http.Serve(ln, http.FileServer(http.Dir(dir)))

	url := "http://" + ln.Addr().String() + "/report.html"
	deadline := time.Now().Add(2 * time.Second)
	var body []byte
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			body, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(body) != "<html>Redline</html>" {
		t.Fatalf("got %q", body)
	}
}

// fakeServer stands in for a running `redline serve` for dir.
func fakeServer(t *testing.T, dir string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	t.Cleanup(func() { _ = ln.Close() })
	mux := http.NewServeMux()
	mux.HandleFunc(idPath, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(serverInfo{PID: 1, Port: port, Dir: dir})
	})
	mux.Handle("/", http.FileServer(http.Dir(dir)))
	go http.Serve(ln, mux)
	return port
}

func reportDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.html"), []byte("<html>Redline</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestOpenDirReusesServerForTheSameReport(t *testing.T) {
	dir := reportDir(t)
	port := fakeServer(t, dir)

	url, err := openDir(dir, false, port)
	if err != nil {
		t.Fatal(err)
	}
	if url != ReportURL(port) {
		t.Fatalf("got %s", url)
	}
}

// The bug this guards: a server answering 200 on the port is not proof it is
// serving *this* review. Reviewing a second repository while the first one's
// server holds the port used to hand the reviewer the wrong report.
func TestOpenDirRejectsAForeignServerOnThePort(t *testing.T) {
	mine := reportDir(t)
	theirs := reportDir(t)
	if err := os.WriteFile(filepath.Join(theirs, "report.html"), []byte("<html>Someone else</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	port := fakeServer(t, theirs)

	if servesDir(port, mine) {
		t.Fatal("a server for another directory was accepted as ours")
	}
	if _, ok := findServer(mine, port); ok {
		t.Fatal("findServer accepted a foreign server")
	}
}

// A plain static server — `python -m http.server` in the evidence directory —
// answers 200 for report.html but cannot identify itself, so it is not ours.
func TestOpenDirRejectsAnUnidentifiedServer(t *testing.T) {
	dir := reportDir(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	t.Cleanup(func() { _ = ln.Close() })
	go http.Serve(ln, http.FileServer(http.Dir(dir)))

	if servesDir(port, dir) {
		t.Fatal("a server with no identity endpoint was accepted as ours")
	}
}

func TestFreePortSkipsABoundPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	t.Cleanup(func() { _ = ln.Close() })

	got, err := freePort(port)
	if err != nil {
		t.Fatal(err)
	}
	if got == port {
		t.Fatalf("freePort returned the bound port %d", port)
	}
}

func TestServeAnswersItsIdentityAndStops(t *testing.T) {
	dir := reportDir(t)
	port, err := freePort(20000)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Serve(dir, port) }()

	if err := waitServing(port, dir, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	info, ok := readPID(dir)
	if !ok || info.Port != port || !sameDir(info.Dir, dir) {
		t.Fatalf("pid file not written for this server: %+v ok=%v", info, ok)
	}
	// Stop must not signal this test process, which is what the pid file
	// names here; it clears the record instead.
	if err := Stop(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPID(dir); ok {
		t.Fatal("pid record survived Stop")
	}
	select {
	case <-done:
	default:
	}
}

func TestStopWithNoRecordIsNotAnError(t *testing.T) {
	if err := Stop(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestStopIgnoresAStaleRecord(t *testing.T) {
	dir := reportDir(t)
	// A record naming a port nothing is listening on: the PID may have been
	// reused, so it must be dropped rather than signalled.
	writePID(dir, serverInfo{PID: 999999, Port: 65533, Dir: dir})
	if err := Stop(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPID(dir); ok {
		t.Fatal("stale pid record survived Stop")
	}
}

func TestOpenDirMissingReport(t *testing.T) {
	_, err := OpenDir(t.TempDir(), false)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTestBinaryName(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/tmp/go-build/redline.test", true},
		{`C:\Users\x\redline.test.exe`, true},
		{"/usr/local/bin/redline", false},
		{"/tmp/report.test", true},
	}
	for _, c := range cases {
		if got := isTestBinaryName(c.path); got != c.want {
			t.Fatalf("%s: got %v want %v", c.path, got, c.want)
		}
	}
}

// If this re-exec'd report.test, the child would run the suite again and
// every announce would spawn more children. The machine would die. Prove
// we refuse instead, and that nothing is listening afterward.
func TestUnderGoTest(t *testing.T) {
	if !underGoTest() {
		t.Fatal("this process is a test binary")
	}
}

func TestStartServeRefusesATestBinary(t *testing.T) {
	dir := reportDir(t)
	port, err := freePort(21000)
	if err != nil {
		t.Fatal(err)
	}
	if err := startServe(dir, port); err == nil {
		t.Fatal("startServe must refuse to re-exec a test binary")
	}
	time.Sleep(100 * time.Millisecond)
	if servesDir(port, dir) {
		t.Fatal("a server started; the test binary was re-exec'd")
	}
}

// reportServer builds a serveHandler over a directory laid out like a real
// evidence directory: report.html, the session.json that carries the diffs,
// and an evidence/ subtree.
func reportServer(t *testing.T) http.Handler {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("report.html", "<html>Redline</html>")
	write("session.json", `{"diff":"SECRET"}`)
	write("evidence/note.txt", "an artifact")
	return serveHandler(dir, serverInfo{PID: 1, Port: 8765, Dir: dir}, newIdleTimer(idleTimeout))
}

func request(h http.Handler, host, path string) *http.Response {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if host != "" {
		req.Host = host
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

// A page on another origin that rebinds DNS to 127.0.0.1 reaches the server
// with its own name in the Host header. Refusing a non-loopback Host is what
// keeps that page from reading the session the server serves.
func TestServeRejectsForeignHost(t *testing.T) {
	h := reportServer(t)
	if r := request(h, "evil.example.com", "/report.html"); r.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Host got %d, want 403", r.StatusCode)
	}
	if r := request(h, "127.0.0.1:8765", "/report.html"); r.StatusCode != http.StatusOK {
		t.Errorf("loopback Host got %d, want 200", r.StatusCode)
	}
}

// session.json holds every diff and file body. Only report.html and the
// evidence/ subtree are on the wire; nothing else in the directory is.
func TestServeServesOnlyReportAndEvidence(t *testing.T) {
	h := reportServer(t)
	if r := request(h, "127.0.0.1", "/session.json"); r.StatusCode == http.StatusOK {
		t.Errorf("session.json was served (status %d); it holds the diffs", r.StatusCode)
	}
	if r := request(h, "127.0.0.1", "/evidence/note.txt"); r.StatusCode != http.StatusOK {
		t.Errorf("evidence artifact not served: status %d", r.StatusCode)
	}
}

// A directory request must not turn into a listing of the artifacts under it.
func TestServeDoesNotListDirectories(t *testing.T) {
	h := reportServer(t)
	r := request(h, "127.0.0.1", "/evidence/")
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode == http.StatusOK && strings.Contains(string(body), "note.txt") {
		t.Errorf("directory request listed its contents:\n%s", body)
	}
}
