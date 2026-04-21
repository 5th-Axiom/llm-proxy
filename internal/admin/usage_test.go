package admin_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestUsageSummaryEndToEnd drives real proxy traffic through the handler,
// lets the async writer flush, and confirms /api/usage/summary returns the
// expected per-user and per-provider roll-ups.
func TestUsageSummaryEndToEnd(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Include a usage block so token counting fills in non-zero numbers
		// once forwarder parses the non-streaming body.
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":7,"completion_tokens":11,"total_tokens":18}}`))
	}))
	defer upstream.Close()

	handlers, _ := bootstrapEmpty(t)
	proxyTS := httptest.NewServer(handlers.Public)
	defer proxyTS.Close()
	adminTS := httptest.NewServer(handlers.Admin)
	defer adminTS.Close()

	// Provision a provider that actually reaches the test upstream.
	postJSON(t, adminTS.URL+"/api/providers",
		`{"name":"echo","type":"openai","base_path":"/echo",
		  "upstream_base_url":"`+upstream.URL+`","upstream_api_key":"k",
		  "token_counting":true}`,
		http.StatusCreated)

	// Create two users with one key each.
	u1 := postJSONAndDecode(t, adminTS.URL+"/api/users", `{"name":"ursula"}`, http.StatusCreated)
	u2 := postJSONAndDecode(t, adminTS.URL+"/api/users", `{"name":"victor"}`, http.StatusCreated)

	u1ID := int64(u1["id"].(float64))
	u2ID := int64(u2["id"].(float64))
	tok1 := postJSONAndDecode(t, adminTS.URL+"/api/users/"+strconv.FormatInt(u1ID, 10)+"/keys",
		`{}`, http.StatusCreated)["token"].(string)
	tok2 := postJSONAndDecode(t, adminTS.URL+"/api/users/"+strconv.FormatInt(u2ID, 10)+"/keys",
		`{}`, http.StatusCreated)["token"].(string)

	// ursula: 2 calls, victor: 1 call.
	fire := func(token string) {
		if code := curlStatus(t, proxyTS.URL+"/echo/v1/chat/completions", token); code != http.StatusOK {
			t.Fatalf("call = %d, want 200", code)
		}
	}
	fire(tok1)
	fire(tok1)
	fire(tok2)

	// The recorder writes asynchronously. Drain so the reads below see the
	// inserts even on fast machines where the goroutine hasn't caught up.
	handlers.Usage.Close()

	// /api/usage/summary defaults to a 7-day window, which covers everything
	// we just wrote.
	summary := getJSONDecode(t, adminTS.URL+"/api/usage/summary")

	users := summary["users"].([]any)
	userByName := map[string]map[string]any{}
	for _, uAny := range users {
		u := uAny.(map[string]any)
		userByName[u["user_name"].(string)] = u
	}
	if ru := userByName["ursula"]; ru == nil || int(ru["requests"].(float64)) != 2 {
		t.Fatalf("ursula usage = %+v, want 2 requests", ru)
	}
	if rv := userByName["victor"]; rv == nil || int(rv["requests"].(float64)) != 1 {
		t.Fatalf("victor usage = %+v, want 1 request", rv)
	}
	// Upstream reported prompt=7, completion=11 per request. ursula = 2 calls.
	if pt := int(userByName["ursula"]["prompt_tokens"].(float64)); pt != 14 {
		t.Fatalf("ursula prompt_tokens = %d, want 14", pt)
	}

	providers := summary["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("providers len = %d, want 1", len(providers))
	}
	pv := providers[0].(map[string]any)
	if pv["provider"].(string) != "echo" || int(pv["requests"].(float64)) != 3 {
		t.Fatalf("provider row = %+v, want echo/3", pv)
	}
}

// TestUsageSummaryRejectsBadWindow checks the window parameter validation.
func TestUsageSummaryRejectsBadWindow(t *testing.T) {
	handlers, _ := bootstrapEmpty(t)
	adminTS := httptest.NewServer(handlers.Admin)
	defer adminTS.Close()

	for _, window := range []string{"-5d", "abc", "hour"} {
		url := adminTS.URL + "/api/usage/summary?window=" + window
		r, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("window=%q status = %d, want 400", window, r.StatusCode)
		}
	}
}

// TestUsageSummaryRespectsSinceQuery verifies the since=RFC3339 override
// filters older rows out of the response.
func TestUsageSummaryRespectsSinceQuery(t *testing.T) {
	handlers, _ := bootstrapEmpty(t)
	adminTS := httptest.NewServer(handlers.Admin)
	defer adminTS.Close()

	// RFC3339 timestamps include a `+` in the timezone offset which a URL
	// would otherwise interpret as a space; query-escape before sending.
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	resp, err := http.Get(adminTS.URL + "/api/usage/summary?since=" + url.QueryEscape(future))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"users":null`) && !strings.Contains(string(body), `"users":[]`) {
		t.Fatalf("since-in-future should return empty users; body=%s", body)
	}
}

func getJSONDecode(t *testing.T, url string) map[string]any {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d; body=%s", url, r.StatusCode, b)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode %s: %v; body=%s", url, err, b)
	}
	return out
}
