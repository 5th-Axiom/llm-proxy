package admin_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestAdminUserKeyLifecycle covers the full "admin creates user → issues key
// → client uses key → admin revokes key" flow via the HTTP API.
func TestAdminUserKeyLifecycle(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	handlers, _ := bootstrapEmpty(t)
	proxyTS := httptest.NewServer(handlers.Public)
	defer proxyTS.Close()
	adminTS := httptest.NewServer(handlers.Admin)
	defer adminTS.Close()

	// Swap the placeholder provider for one that actually reaches upstream.
	postJSON(t, adminTS.URL+"/api/providers",
		`{"name":"ok","type":"openai","base_path":"/ok",
		  "upstream_base_url":"`+upstream.URL+`","upstream_api_key":"k"}`,
		http.StatusCreated)

	// --- 1. Create user ---
	createUserResp := postJSONAndDecode(t, adminTS.URL+"/api/users",
		`{"name":"alice","email":"alice@example.com"}`, http.StatusCreated)
	userID := int64(createUserResp["id"].(float64))
	if userID <= 0 {
		t.Fatalf("user id = %v, want > 0", createUserResp["id"])
	}

	// --- 2. List users ---
	listUsersBody := getJSON(t, adminTS.URL+"/api/users")
	if !strings.Contains(listUsersBody, `"name":"alice"`) {
		t.Fatalf("list users missing alice:\n%s", listUsersBody)
	}

	// --- 3. Issue a key ---
	issueResp := postJSONAndDecode(t, adminTS.URL+"/api/users/"+strconv.FormatInt(userID, 10)+"/keys",
		`{"name":"laptop"}`, http.StatusCreated)
	token, _ := issueResp["token"].(string)
	tokenPrefix, _ := issueResp["token_prefix"].(string)
	if !strings.HasPrefix(token, "llmp_") {
		t.Fatalf("issued token prefix = %q, want llmp_", token)
	}
	if !strings.HasPrefix(tokenPrefix, "llmp_") || len(tokenPrefix) >= len(token) {
		t.Fatalf("token_prefix = %q, want visible-only prefix shorter than full token", tokenPrefix)
	}

	// --- 4. Listing keys must NOT leak the plaintext token ---
	keysList := getJSON(t, adminTS.URL+"/api/users/"+strconv.FormatInt(userID, 10)+"/keys")
	if strings.Contains(keysList, token) {
		t.Fatalf("keys list leaked plaintext token:\n%s", keysList)
	}
	if !strings.Contains(keysList, tokenPrefix) {
		t.Fatalf("keys list missing token_prefix %q:\n%s", tokenPrefix, keysList)
	}

	// --- 5. Use the key against the proxy ---
	if code := curlStatus(t, proxyTS.URL+"/ok/v1/anything", token); code != http.StatusOK {
		t.Fatalf("proxy call with fresh key = %d, want 200", code)
	}

	// --- 6. Revoke the key ---
	deleteReq(t, adminTS.URL+"/api/keys/"+tokenPrefix)

	// --- 7. Revoked key is rejected ---
	if code := curlStatus(t, proxyTS.URL+"/ok/v1/anything", token); code != http.StatusUnauthorized {
		t.Fatalf("proxy call with revoked key = %d, want 401", code)
	}

	// --- 8. Disabling the user rejects a *new* key too ---
	issueResp2 := postJSONAndDecode(t, adminTS.URL+"/api/users/"+strconv.FormatInt(userID, 10)+"/keys",
		`{"name":"second"}`, http.StatusCreated)
	token2 := issueResp2["token"].(string)
	if code := curlStatus(t, proxyTS.URL+"/ok/v1/anything", token2); code != http.StatusOK {
		t.Fatalf("pre-disable call = %d, want 200", code)
	}
	deleteReq(t, adminTS.URL+"/api/users/"+strconv.FormatInt(userID, 10))
	if code := curlStatus(t, proxyTS.URL+"/ok/v1/anything", token2); code != http.StatusUnauthorized {
		t.Fatalf("post-disable call = %d, want 401", code)
	}
}

// TestAdminUserDuplicateNameRejected checks the conflict handling when two
// users would collide on name.
func TestAdminUserDuplicateNameRejected(t *testing.T) {
	handlers, _ := bootstrapEmpty(t)
	adminTS := httptest.NewServer(handlers.Admin)
	defer adminTS.Close()

	postJSON(t, adminTS.URL+"/api/users", `{"name":"dup"}`, http.StatusCreated)
	r, err := http.Post(adminTS.URL+"/api/users", "application/json",
		strings.NewReader(`{"name":"dup"}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate name status = %d, want 409", r.StatusCode)
	}
}

func postJSONAndDecode(t *testing.T, url, body string, wantStatus int) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("POST %s status = %d, want %d; body=%s", url, resp.StatusCode, wantStatus, rb)
	}
	var out map[string]any
	if err := json.Unmarshal(rb, &out); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rb)
	}
	return out
}
