package report

import (
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

func TestOpenDirReusesExistingServer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.html"), []byte("<html>Redline</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	t.Cleanup(func() { _ = ln.Close() })
	go http.Serve(ln, http.FileServer(http.Dir(dir)))

	url, err := openDir(dir, false, port)
	if err != nil {
		t.Fatal(err)
	}
	if url != ReportURL(port) {
		t.Fatalf("got %s", url)
	}
}

func TestOpenDirMissingReport(t *testing.T) {
	_, err := OpenDir(t.TempDir(), false)
	if err == nil {
		t.Fatal("expected error")
	}
}
