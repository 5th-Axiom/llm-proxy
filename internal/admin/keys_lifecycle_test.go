package admin_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestKeyRevealAndDelete covers the plaintext-reveal + revoke-then-delete
// path introduced in the "view + copy" admin UX. It walks a key through:
//   - issue (plaintext_present flips to true)
//   - reveal returns the same plaintext issue handed back
//   - delete of an active key is rejected (409) — revoke first
//   - revoke
//   - delete succeeds and the row is gone from the list
func TestKeyRevealAndDelete(t *testing.T) {
	handlers, _ := bootstrapEmpty(t)
	adminTS := httptest.NewServer(handlers.Admin)
	defer adminTS.Close()

	// Fresh user.
	userResp := postJSONAndDecode(t, adminTS.URL+"/api/users",
		`{"name":"reveal-demo"}`, http.StatusCreated)
	userID := int64(userResp["id"].(float64))

	// Issue a key.
	issued := postJSONAndDecode(t, adminTS.URL+"/api/users/"+strconv.FormatInt(userID, 10)+"/keys",
		`{"name":"demo"}`, http.StatusCreated)
	token := issued["token"].(string)
	prefix := issued["token_prefix"].(string)

	// Listing exposes plaintext_present = true.
	listBody := getJSON(t, adminTS.URL+"/api/users/"+strconv.FormatInt(userID, 10)+"/keys")
	if !strings.Contains(listBody, `"plaintext_present":true`) {
		t.Fatalf("list missing plaintext_present=true:\n%s", listBody)
	}

	// Reveal roundtrip.
	reveal := getJSONDecode(t, adminTS.URL+"/api/keys/"+prefix+"/reveal")
	if reveal["token"].(string) != token {
		t.Fatalf("reveal token = %q, want %q", reveal["token"], token)
	}

	// DELETE before revoke should 409.
	req, _ := http.NewRequest(http.MethodDelete, adminTS.URL+"/api/keys/"+prefix, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete-active status = %d, want 409", resp.StatusCode)
	}

	// Revoke.
	postJSON(t, adminTS.URL+"/api/keys/"+prefix+"/revoke", "", http.StatusNoContent)

	// Hard delete now works.
	deleteReq(t, adminTS.URL+"/api/keys/"+prefix)

	// Reveal on a deleted prefix is 404.
	resp, err = http.Get(adminTS.URL + "/api/keys/" + prefix + "/reveal")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("reveal-after-delete status = %d, want 404", resp.StatusCode)
	}

	// Key is gone from the list.
	listBody = getJSON(t, adminTS.URL+"/api/users/"+strconv.FormatInt(userID, 10)+"/keys")
	if strings.Contains(listBody, prefix) {
		t.Fatalf("deleted key still listed:\n%s", listBody)
	}
}
