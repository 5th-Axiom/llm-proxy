package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"llm-proxy/internal/store"
)

// userView is the serialised shape of a user over the API. DisabledAt uses a
// pointer so the JSON cleanly distinguishes "active" (null) from a concrete
// disabled timestamp.
type userView struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Email      string     `json:"email,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	DisabledAt *time.Time `json:"disabled_at,omitempty"`
	KeyCount   int        `json:"key_count"`
}

type userInput struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Disabled *bool  `json:"disabled,omitempty"`
}

func toUserView(u store.User) userView {
	return userView{
		ID:         u.ID,
		Name:       u.Name,
		Email:      u.Email,
		CreatedAt:  u.CreatedAt,
		DisabledAt: u.DisabledAt,
		KeyCount:   u.KeyCount,
	}
}

// handleUsersCollection implements GET /api/users (list) and POST /api/users
// (create).
func (h *Handler) handleUsersCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		users, err := h.container.Store().ListUsers(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]userView, 0, len(users))
		for _, u := range users {
			out = append(out, toUserView(u))
		}
		writeJSON(w, http.StatusOK, out)

	case http.MethodPost:
		var in userInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		user, err := h.container.Store().CreateUser(r.Context(), name, strings.TrimSpace(in.Email))
		if err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				http.Error(w, "user name already exists", http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.logger.Info("admin: user created", "name", user.Name, "id", user.ID)
		writeJSON(w, http.StatusCreated, toUserView(*user))

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUserItem dispatches /api/users/:id/... This handler routes both the
// user detail endpoints and the nested /keys subtree so it owns parsing of
// the path segments.
func (h *Handler) handleUserItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/users/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}

	switch {
	case len(parts) == 1:
		h.handleUser(w, r, id)
	case len(parts) == 2 && parts[1] == "keys":
		h.handleUserKeys(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleUser(w http.ResponseWriter, r *http.Request, id int64) {
	switch r.Method {
	case http.MethodGet:
		user, err := h.container.Store().GetUser(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, toUserView(*user))

	case http.MethodPatch:
		var in userInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Fetch first so a PATCH that only changes one field doesn't clobber
		// the others with empty strings.
		current, err := h.container.Store().GetUser(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = current.Name
		}
		email := strings.TrimSpace(in.Email)
		if email == "" && in.Email == "" {
			email = current.Email
		}
		if _, err := h.container.Store().UpdateUser(r.Context(), id, name, email); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				http.Error(w, "user name already exists", http.StatusConflict)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if in.Disabled != nil {
			if err := h.container.Store().SetUserDisabled(r.Context(), id, *in.Disabled); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		updated, _ := h.container.Store().GetUser(r.Context(), id)
		h.logger.Info("admin: user updated", "id", id)
		writeJSON(w, http.StatusOK, toUserView(*updated))

	case http.MethodDelete:
		// DELETE disables the user rather than hard-deleting. Preserves
		// audit trail in usage_records and keeps foreign keys intact.
		if err := h.container.Store().SetUserDisabled(r.Context(), id, true); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.logger.Info("admin: user disabled", "id", id)
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
