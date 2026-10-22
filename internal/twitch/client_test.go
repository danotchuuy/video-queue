package twitch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testID     = "cid"
	testSecret = "s3cr3t-value"
)

// fakeTwitch serves both the auth and Helix endpoints.
type fakeTwitch struct {
	mu        sync.Mutex
	tokens    int        // tokens issued
	batches   [][]string // ids per Helix request
	known     map[string]bool
	expiresIn int
	failAuth  func(n int) bool // reject Helix requests with 401 when true
	status    func(n int, w http.ResponseWriter) bool
	helixReqs int
	srv       *httptest.Server
}

func newFake(t *testing.T) (*fakeTwitch, *Client) {
	f := &fakeTwitch{known: map[string]bool{}, expiresIn: 3600}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Method != http.MethodPost || r.Form.Get("client_id") != testID ||
			r.Form.Get("client_secret") != testSecret || r.Form.Get("grant_type") != "client_credentials" {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.tokens++
		n := f.tokens
		f.mu.Unlock()
		fmt.Fprintf(w, `{"access_token":"tok%d","expires_in":%d}`, n, f.expiresIn)
	})
	mux.HandleFunc("/api/clips", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.helixReqs++
		n := f.helixReqs
		cur := fmt.Sprintf("tok%d", f.tokens)
		f.mu.Unlock()
		if r.Header.Get("Client-Id") != testID {
			http.Error(w, "no client id", http.StatusBadRequest)
			return
		}
		if f.status != nil && f.status(n, w) {
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+cur || (f.failAuth != nil && f.failAuth(n)) {
			http.Error(w, "unauthorized, token "+r.Header.Get("Authorization")+" secret "+testSecret, http.StatusUnauthorized)
			return
		}
		ids := r.URL.Query()["id"]
		f.mu.Lock()
		f.batches = append(f.batches, ids)
		f.mu.Unlock()
		var data []Clip
		// Reply in reverse order to prove the client restores input order.
		for i := len(ids) - 1; i >= 0; i-- {
			if f.known[ids[i]] {
				data = append(data, Clip{ID: ids[i], Title: "t-" + ids[i]})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	c := NewClient(testID, testSecret)
	c.apiBase, c.authBase = f.srv.URL+"/api", f.srv.URL+"/auth"
	return f, c
}

func ids(clips []Clip) string {
	var s []string
	for _, c := range clips {
		s = append(s, c.ID)
	}
	return strings.Join(s, ",")
}

func TestTokenFetchedOnceAndReused(t *testing.T) {
	f, c := newFake(t)
	f.known["a"] = true
	for range 3 {
		if _, err := c.Clips(context.Background(), []string{"a"}); err != nil {
			t.Fatal(err)
		}
	}
	if f.tokens != 1 {
		t.Errorf("tokens fetched = %d, want 1", f.tokens)
	}
}

func TestTokenRefreshedNearExpiry(t *testing.T) {
	f, c := newFake(t)
	f.known["a"] = true
	now := time.Now()
	c.now = func() time.Time { return now }
	f.expiresIn = 300
	if _, err := c.Clips(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(200 * time.Second) // still 100s left
	if _, err := c.Clips(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if f.tokens != 1 {
		t.Fatalf("tokens = %d, want 1 before the leeway", f.tokens)
	}
	now = now.Add(50 * time.Second) // 50s left: inside the 60s leeway
	if _, err := c.Clips(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if f.tokens != 2 {
		t.Errorf("tokens = %d, want 2 after refresh", f.tokens)
	}
}

func TestReauthOn401(t *testing.T) {
	f, c := newFake(t)
	f.known["a"] = true
	if _, err := c.Clips(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	f.failAuth = func(n int) bool { return n == 2 } // token revoked once
	got, err := c.Clips(context.Background(), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if ids(got) != "a" || f.tokens != 2 || f.helixReqs != 3 {
		t.Errorf("got %q, tokens %d, helix requests %d; want a, 2, 3", ids(got), f.tokens, f.helixReqs)
	}
}

func TestPersistent401GivesUpAfterOneRetry(t *testing.T) {
	f, c := newFake(t)
	f.failAuth = func(int) bool { return true }
	_, err := c.Clips(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("want error")
	}
	if f.helixReqs != 2 || f.tokens != 2 {
		t.Errorf("helix requests %d, tokens %d; want 2, 2", f.helixReqs, f.tokens)
	}
	for _, secret := range []string{testSecret, "tok1", "tok2"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks %q: %v", secret, err)
		}
	}
}

func TestBatching(t *testing.T) {
	f, c := newFake(t)
	var slugs []string
	for i := range 250 {
		s := "c" + strconv.Itoa(i)
		slugs = append(slugs, s)
		if i%2 == 0 {
			f.known[s] = true
		}
	}
	got, err := c.Clips(context.Background(), slugs)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.batches) != 3 || len(f.batches[0]) != 100 || len(f.batches[1]) != 100 || len(f.batches[2]) != 50 {
		t.Errorf("batches sized %d, want 100/100/50", len(f.batches))
	}
	if len(got) != 125 {
		t.Fatalf("got %d clips, want 125", len(got))
	}
	for i, cl := range got {
		if want := "c" + strconv.Itoa(i*2); cl.ID != want {
			t.Fatalf("clip %d = %s, want %s", i, cl.ID, want)
		}
	}
}

func TestOrderAndOmission(t *testing.T) {
	f, c := newFake(t)
	f.known["a"], f.known["b"], f.known["d"] = true, true, true
	got, err := c.Clips(context.Background(), []string{"d", "x", "a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if ids(got) != "d,a,b" {
		t.Errorf("got %s, want d,a,b", ids(got))
	}
}

func TestEmptyInputMakesNoRequest(t *testing.T) {
	f, c := newFake(t)
	got, err := c.Clips(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
	if f.tokens != 0 || f.helixReqs != 0 {
		t.Errorf("tokens %d, helix %d; want no requests", f.tokens, f.helixReqs)
	}
}

func TestRetryOn429(t *testing.T) {
	f, c := newFake(t)
	f.known["a"] = true
	f.status = func(n int, w http.ResponseWriter) bool {
		if n == 1 {
			w.Header().Set("Ratelimit-Reset", strconv.FormatInt(time.Now().Add(150*time.Millisecond).Unix(), 10))
			w.WriteHeader(http.StatusTooManyRequests)
			return true
		}
		return false
	}
	got, err := c.Clips(context.Background(), []string{"a"})
	if err != nil || ids(got) != "a" {
		t.Fatalf("got %v, %v", got, err)
	}
	if f.helixReqs != 2 {
		t.Errorf("helix requests = %d, want 2", f.helixReqs)
	}
}

func TestSecond429IsAnError(t *testing.T) {
	f, c := newFake(t)
	f.status = func(n int, w http.ResponseWriter) bool {
		w.Header().Set("Ratelimit-Reset", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	}
	if _, err := c.Clips(context.Background(), []string{"a"}); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v, want a 429 error", err)
	}
	if f.helixReqs != 2 {
		t.Errorf("helix requests = %d, want 2", f.helixReqs)
	}
}

func TestRateWaitCapped(t *testing.T) {
	c := NewClient(testID, testSecret)
	h := http.Header{}
	h.Set("Ratelimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
	if got := c.rateWait(h); got != maxRateWait {
		t.Errorf("wait = %v, want %v", got, maxRateWait)
	}
}

func TestContextCancelDuringRateWait(t *testing.T) {
	f, c := newFake(t)
	f.status = func(n int, w http.ResponseWriter) bool {
		w.Header().Set("Ratelimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Clips(ctx, []string{"a"}); err != context.DeadlineExceeded {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("did not return promptly")
	}
}

func TestErrorsNeverLeakSecretOrToken(t *testing.T) {
	f, c := newFake(t)
	f.status = func(n int, w http.ResponseWriter) bool {
		http.Error(w, "boom "+testSecret+" tok1", http.StatusInternalServerError)
		return true
	}
	_, err := c.Clips(context.Background(), []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want status 500", err)
	}
	for _, secret := range []string{testSecret, "tok1"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks %q: %v", secret, err)
		}
	}
}

func TestAuthFailureDoesNotLeakSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		http.Error(w, "invalid client_secret="+r.Form.Get("client_secret"), http.StatusForbidden)
	}))
	defer srv.Close()
	c := NewClient(testID, testSecret)
	c.apiBase, c.authBase = srv.URL, srv.URL
	_, err := c.Clips(context.Background(), []string{"a"})
	if err == nil || strings.Contains(err.Error(), testSecret) {
		t.Fatalf("err = %v", err)
	}
}
