package report

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sendBody = `{"redlineReviewComments":{"target":"PR #1","comments":[{"action":"comment","file":"a.go","line":3,"text":"why"}]}}`

// agentServer is reportServer with agent.json in place, and herdrPrompt
// replaced by a recorder.
func agentServer(t *testing.T, sendErr error) (http.Handler, *[]string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "w5:p1")
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr.sock")
	if err := RecordAgent(dir); err != nil {
		t.Fatal(err)
	}
	var sent []string
	orig := herdrPrompt
	herdrPrompt = func(_ context.Context, a agentTarget, text string) error {
		sent = append(sent, a.Pane+" "+a.Socket+" "+text)
		return sendErr
	}
	t.Cleanup(func() { herdrPrompt = orig })
	return serveHandler(dir, serverInfo{PID: 1, Port: 8765, Dir: dir}, newIdleTimer(idleTimeout)), &sent
}

func postSend(h http.Handler, origin, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/agent/send", strings.NewReader(body))
	req.Host = "127.0.0.1:8765"
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAgentSendPromptsTheRecordedPane(t *testing.T) {
	h, sent := agentServer(t, nil)
	rec := postSend(h, "http://127.0.0.1:8765", "application/json", sendBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(*sent) != 1 || (*sent)[0] != "w5:p1 /tmp/herdr.sock "+sendBody {
		t.Errorf("sent %q", *sent)
	}
	if r := request(h, "127.0.0.1:8765", "/agent"); r.StatusCode != http.StatusOK {
		t.Errorf("/agent got %d, want 200", r.StatusCode)
	}
}

// Any web page can POST to 127.0.0.1, and the Host header it arrives with is
// loopback. What arrives at /agent/send is typed into an agent that can run
// commands, so only the report page itself may send.
func TestAgentSendRefusesOtherPages(t *testing.T) {
	h, sent := agentServer(t, nil)
	for _, origin := range []string{"", "https://evil.example.com", "http://127.0.0.1:9999", "null"} {
		if rec := postSend(h, origin, "application/json", sendBody); rec.Code != http.StatusForbidden {
			t.Errorf("Origin %q got %d, want 403", origin, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/agent/send", strings.NewReader(sendBody))
	req.Host = "127.0.0.1:8765"
	req.Header.Set("Origin", "http://127.0.0.1:8765")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("Sec-Fetch-Site cross-site got %d, want 403", rec.Code)
	}
	// text/plain is what a cross-origin form or no-cors fetch can send
	// without a preflight.
	if rec := postSend(h, "http://127.0.0.1:8765", "text/plain", sendBody); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain got %d, want 415", rec.Code)
	}
	if rec := postSend(h, "http://127.0.0.1:8765", "application/json", `{"anything":"else"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("foreign JSON got %d, want 400", rec.Code)
	}
	if len(*sent) != 0 {
		t.Errorf("refused requests still reached herdr: %q", *sent)
	}
}

func TestAgentSendReportsHerdrErrors(t *testing.T) {
	h, _ := agentServer(t, errors.New("agent is waiting on a dialog (agent_blocked)"))
	rec := postSend(h, "http://127.0.0.1:8765", "application/json", sendBody)
	var got map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusBadGateway || !strings.Contains(got["error"], "agent_blocked") {
		t.Errorf("got %d %v, want 502 with herdr's message", rec.Code, got)
	}
}

// A run started outside herdr removes the record an earlier run left, so the
// page does not offer to send to a pane that may now hold something else.
func TestRecordAgentOutsideHerdrRemovesTheRecord(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	if err := RecordAgent(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_ENV", "")
	t.Setenv("HERDR_PANE_ID", "")
	if err := RecordAgent(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, agentFile)); !os.IsNotExist(err) {
		t.Errorf("agent.json survived a run outside herdr: %v", err)
	}
	if _, ok := readAgent(dir); ok {
		t.Error("readAgent found a target after removal")
	}
}

func TestAgentAbsentWithoutARecord(t *testing.T) {
	h := reportServer(t)
	if r := request(h, "127.0.0.1", "/agent"); r.StatusCode != http.StatusNotFound {
		t.Errorf("/agent got %d without agent.json, want 404", r.StatusCode)
	}
}
