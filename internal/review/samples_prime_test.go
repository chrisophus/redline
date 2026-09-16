package review

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func primeReviewJSON(n int) string {
	return fmt.Sprintf(`{"overview":"a","files":[],"comments":[{"file":"f%d.go","line":%d,`+
		`"severity":"warning","confidence":"high","body":"defect number %d in its own file"}]}`, n, n+1, n)
}

func primeOpts(url string, cache bool) Options {
	return Options{
		Model: "claude-sonnet-5", Mode: ModeOneShot, API: APIAnthropic,
		BaseURL: url, APIKey: "test", MaxTokens: 4096, MaxCostUSD: 5,
		Samples: 3, Cache: cache, CacheTTL: CacheTTL5m,
	}
}

// With the cache on, no sample but the first may reach the endpoint before
// the first one's stream carries content. Content is the proof the prompt was
// read and the entry written; a sample sent sooner pays to read the prompt
// again, which on a 247k-token prompt is most of what a sample costs.
func TestTheFirstSamplePrimesTheCacheForTheRest(t *testing.T) {
	var mu sync.Mutex
	var requests, early int
	var unmarked []int
	contentSent := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		n := requests
		requests++
		if n > 0 && !contentSent {
			early++
		}
		if !strings.Contains(string(body), "cache_control") {
			unmarked = append(unmarked, n)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		reply := anthropicSSE("tool_use", 100, 10, flatCalls(StageReview, primeReviewJSON(n)))
		if n == 0 {
			// message_start alone, then a pause the rest would land in if they
			// had not waited, then the content.
			cut := strings.Index(reply, "event: content_block_start")
			fmt.Fprint(w, reply[:cut])
			flusher.Flush()
			time.Sleep(200 * time.Millisecond)
			mu.Lock()
			contentSent = true
			mu.Unlock()
			fmt.Fprint(w, reply[cut:])
			flusher.Flush()
			return
		}
		fmt.Fprint(w, reply)
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)

	res, err := Run(context.Background(), exploreInput(), primeOpts(srv.URL, true))
	if err != nil {
		t.Fatalf("samples: %v", err)
	}
	if early > 0 {
		t.Errorf("%d sample(s) went out before the first had read the prompt", early)
	}
	if len(unmarked) > 0 {
		t.Errorf("requests %v carried no cache breakpoint, so they could read no entry", unmarked)
	}
	if len(res.Review.Comments) != 3 || res.SamplesFailed != 0 {
		t.Errorf("comments = %d failed = %d, want all three samples kept", len(res.Review.Comments), res.SamplesFailed)
	}
}

// With the cache off there is nothing to prime, so waiting would only add the
// first sample's read time to the run. The first request is held until a
// second one arrives; if the samples were sent one after another it never
// would.
func TestSamplesWithTheCacheOffGoOutTogether(t *testing.T) {
	var mu sync.Mutex
	var requests int
	second := make(chan struct{})
	waitedAlone := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		n := requests
		requests++
		mu.Unlock()
		if n == 1 {
			close(second)
		}
		if n == 0 {
			select {
			case <-second:
			case <-time.After(2 * time.Second):
				mu.Lock()
				waitedAlone = true
				mu.Unlock()
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, anthropicSSE("tool_use", 100, 10, flatCalls(StageReview, primeReviewJSON(n))))
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(srv.Close)

	if _, err := Run(context.Background(), exploreInput(), primeOpts(srv.URL, false)); err != nil {
		t.Fatalf("samples: %v", err)
	}
	if waitedAlone {
		t.Error("with the cache off the samples went out one after another")
	}
}

// A primer that fails before any content still lets the rest go, and the
// review is the union of the ones that answered.
func TestAFailedPrimerReleasesTheRest(t *testing.T) {
	var mu sync.Mutex
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		n := requests
		requests++
		mu.Unlock()
		if n == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"primer refused"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, anthropicSSE("tool_use", 100, 10, flatCalls(StageReview, primeReviewJSON(n))))
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(srv.Close)

	done := make(chan struct{})
	var res *Result
	var err error
	go func() {
		res, err = Run(context.Background(), exploreInput(), primeOpts(srv.URL, true))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the rest never went out after the primer failed")
	}
	if err != nil {
		t.Fatalf("one failed primer must not fail the review: %v", err)
	}
	if res.SamplesFailed != 1 || len(res.Review.Comments) != 2 {
		t.Errorf("failed = %d comments = %d, want the primer counted failed and the other two kept",
			res.SamplesFailed, len(res.Review.Comments))
	}
}
