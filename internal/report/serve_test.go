package report

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
			if resp.StatusCode == 200 {
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
