package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"llm-proxy/internal/store"
)

type keyView struct {
	ID               int64      `json:"id"`
	UserID           int64      `json:"user_id"`
	Name             string     `json:"name"`
	TokenPrefix      string     `json:"token_prefix"`
	CreatedAt        time.Time  `json:"created_at"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	PlaintextPresent bool       `json:"plaintext_present"`
}

// keyCreatedView is returned once, on POST .../keys. It is the only response
// shape that includes the plaintext Token — the admin UI must show-and-copy
// it before the user navigates away.
type keyCreatedView struct {
	keyView
	Token string `json:"token"`
}

func toKeyView(k store.APIKey) keyView {
	return keyView{
		ID:               k.ID,
		UserID:           k.UserID,
		Name:             k.Name,
		TokenPrefix:      k.TokenPrefix,
		CreatedAt:        k.CreatedAt,
		LastUsedAt:       k.LastUsedAt,
		RevokedAt:        k.RevokedAt,
		PlaintextPresent: k.PlaintextPresent,
	}
}

// handleUserKeys implements GET /api/users/:id/keys (list) and POST
// /api/users/:id/keys (issue a fresh key).
func (h *Handler) handleUserKeys(w http.ResponseWriter, r *http.Request, userID int64) {
	switch r.Method {
	case http.MethodGet:
		keys, err := h.container.Store().ListKeysForUser(r.Context(), userID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]keyView, 0, len(keys))
		for _, k := range keys {
			out = append(out, toKeyView(k))
		}
		writeJSON(w, http.StatusOK, out)

	case http.MethodPost:
		var in struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err.Error() != "EOF" {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			// A blank name is valid in the DB (NOT NULL but '' is allowed),
			// but for human usability we default to a readable placeholder.
			name = "default"
		}

		// Confirm the user exists first so we don't return a confusing
		// DB-level foreign-key error.
		if _, err := h.container.Store().GetUser(r.Context(), userID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		issued, err := h.container.Store().IssueKey(r.Context(), userID, name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.logger.Info("admin: key issued", "user_id", userID, "prefix", issued.TokenPrefix)
		writeJSON(w, http.StatusCreated, keyCreatedView{
			keyView: toKeyView(issued.APIKey),
			Token:   issued.Token,
		})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleKeyItem dispatches the per-key endpoints:
//
//	POST   /api/keys/:prefix/revoke   mark revoked (reversible: leaves audit row)
//	GET    /api/keys/:prefix/reveal   return stored plaintext if present
//	DELETE /api/keys/:prefix          hard-delete; requires the key to be revoked first
//
// Identifying by visible prefix (instead of numeric id) keeps the URL
// self-descriptive and avoids putting secret fragments in logs.
func (h *Handler) handleKeyItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/keys/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}

	parts := strings.Split(rest, "/")
	prefix := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	} else if len(parts) > 2 {
		http.NotFound(w, r)
		return
	}

	switch {
	case action == "revoke" && r.Method == http.MethodPost:
		h.revokeKey(w, r, prefix)
	case action == "reveal" && r.Method == http.MethodGet:
		h.revealKey(w, r, prefix)
	case action == "" && r.Method == http.MethodDelete:
		h.deleteKey(w, r, prefix)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request, prefix string) {
	if err := h.container.Store().RevokeKey(r.Context(), prefix); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logger.Info("admin: key revoked", "prefix", prefix)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request, prefix string) {
	err := h.container.Store().DeleteKey(r.Context(), prefix)
	switch {
	case err == nil:
		h.logger.Info("admin: key deleted", "prefix", prefix)
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, store.ErrKeyNotRevoked):
		http.Error(w, "key must be revoked before it can be deleted", http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) revealKey(w http.ResponseWriter, r *http.Request, prefix string) {
	plaintext, err := h.container.Store().GetKeyPlaintext(r.Context(), prefix)
	switch {
	case err == nil:
		h.logger.Info("admin: key plaintext revealed", "prefix", prefix)
		writeJSON(w, http.StatusOK, map[string]string{"token": plaintext})
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, store.ErrKeyPlaintextUnavailable):
		http.Error(w, "plaintext unavailable for this key (predates plaintext storage)", http.StatusGone)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
