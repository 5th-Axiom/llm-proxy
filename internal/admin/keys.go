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
	ID          int64      `json:"id"`
	UserID      int64      `json:"user_id"`
	Name        string     `json:"name"`
	TokenPrefix string     `json:"token_prefix"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
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
		ID:          k.ID,
		UserID:      k.UserID,
		Name:        k.Name,
		TokenPrefix: k.TokenPrefix,
		CreatedAt:   k.CreatedAt,
		LastUsedAt:  k.LastUsedAt,
		RevokedAt:   k.RevokedAt,
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

// handleKeyItem implements DELETE /api/keys/:prefix. We identify keys by
// their visible prefix ("llmp_...") rather than database ID so the URL is
// self-descriptive and an accidental paste into a log won't divulge the
// secret.
func (h *Handler) handleKeyItem(w http.ResponseWriter, r *http.Request) {
	prefix := strings.TrimPrefix(r.URL.Path, "/api/keys/")
	if prefix == "" || strings.Contains(prefix, "/") {
		http.NotFound(w, r)
		return
	}

	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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
