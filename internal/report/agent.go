package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// agentFile records the herdr pane that ran `redline run`. When it is there,
// the report page can send its queued comments straight to the agent in that
// pane instead of making the reviewer copy and paste them.
const agentFile = "agent.json"

// maxSend bounds what the page may send. The payload is a list of comments a
// person wrote, so anything near this size is not one.
const maxSend = 256 << 10

// agentTarget is the agentFile payload. The pane ID only means something to
// the herdr server that issued it, so the socket that reaches that server is
// kept with it.
type agentTarget struct {
	Pane   string `json:"herdrPane"`
	Socket string `json:"herdrSocket,omitempty"`
}

// RecordAgent writes agentFile into dir when this process runs in a herdr
// pane, and removes it when it does not. A record left from an earlier run
// names a pane that may hold something else by now.
func RecordAgent(dir string) error {
	path := filepath.Join(dir, agentFile)
	pane := os.Getenv("HERDR_PANE_ID")
	if os.Getenv("HERDR_ENV") != "1" || pane == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	buf, err := json.Marshal(agentTarget{Pane: pane, Socket: os.Getenv("HERDR_SOCKET_PATH")})
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(buf, '\n'), 0o644)
}

func readAgent(abs string) (agentTarget, bool) {
	buf, err := os.ReadFile(filepath.Join(abs, agentFile))
	if err != nil {
		return agentTarget{}, false
	}
	var t agentTarget
	if err := json.Unmarshal(buf, &t); err != nil || t.Pane == "" {
		return agentTarget{}, false
	}
	return t, true
}

// herdrPrompt submits text to the agent in pane. It is a variable so tests
// can see what would have been sent without a herdr server.
var herdrPrompt = func(ctx context.Context, t agentTarget, text string) error {
	bin, err := exec.LookPath("herdr")
	if err != nil {
		return errors.New("herdr is not on the report server's PATH")
	}
	cmd := exec.CommandContext(ctx, bin, "agent", "prompt", t.Pane, text)
	cmd.Env = os.Environ()
	if t.Socket != "" {
		cmd.Env = append(cmd.Env, "HERDR_SOCKET_PATH="+t.Socket)
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	// herdr reports server errors as {"error":{"code":..,"message":..}}.
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(out, &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("%s (%s)", e.Error.Message, e.Error.Code)
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return errors.New(msg)
	}
	return err
}

// handleAgent answers the page's question of whether there is an agent to
// send to. A 404 hides the button.
func handleAgent(abs string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, ok := readAgent(abs)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]string{"pane": t.Pane})
	}
}

// handleAgentSend types the page's payload into the agent's pane. Whatever
// arrives here becomes a prompt to an agent that can run commands, so the
// request has to come from the report page itself. The loopback Host check in
// serveHandler does not cover that: any web page can POST to 127.0.0.1, and
// its request carries a loopback Host. What it cannot do is send its own
// origin as ours, or send JSON cross-origin without a preflight this server
// never approves.
func handleAgentSend(abs string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			sendError(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if !fromThisPage(r) {
			sendError(w, http.StatusForbidden, "request did not come from the report page")
			return
		}
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			sendError(w, http.StatusUnsupportedMediaType, "send application/json")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxSend+1))
		if err != nil {
			sendError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(body) > maxSend {
			sendError(w, http.StatusRequestEntityTooLarge, "payload too large")
			return
		}
		var probe struct {
			Comments json.RawMessage `json:"redlineReviewComments"`
		}
		if json.Unmarshal(body, &probe) != nil || len(probe.Comments) == 0 {
			sendError(w, http.StatusBadRequest, "not a redlineReviewComments payload")
			return
		}
		t, ok := readAgent(abs)
		if !ok {
			sendError(w, http.StatusNotFound, "this run was not started from a herdr pane")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := herdrPrompt(ctx, t, string(body)); err != nil {
			sendError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]string{"pane": t.Pane})
	}
}

// fromThisPage reports whether r was sent by a page served from this server.
// Browsers set Origin on every POST from fetch, and Sec-Fetch-Site when they
// support it; a request with neither did not come from the page.
func fromThisPage(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	u, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || u.Scheme != "http" {
		return false
	}
	return u.Host == r.Host && loopbackHost(u.Host)
}

func sendError(w http.ResponseWriter, status int, msg string) {
	writeJSONResponse(w, status, map[string]string{"error": msg})
}

func writeJSONResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
