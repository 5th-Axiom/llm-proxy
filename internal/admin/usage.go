package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"llm-proxy/internal/store"
)

type userUsageView struct {
	UserID           int64  `json:"user_id"`
	UserName         string `json:"user_name"`
	Requests         int64  `json:"requests"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
}

type providerUsageView struct {
	Provider         string `json:"provider"`
	Requests         int64  `json:"requests"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
}

type usageSummaryView struct {
	Since      time.Time           `json:"since"`
	WindowDays int                 `json:"window_days"`
	Users      []userUsageView     `json:"users"`
	Providers  []providerUsageView `json:"providers"`
}

// handleUsageSummary returns per-user and per-provider roll-ups over a
// configurable time window. Supported query params:
//
//	window — duration string like "7d", "24h", "30m" (default "7d")
//	since  — explicit RFC3339 timestamp; takes precedence over window
//
// The two aggregation queries share an index on recorded_at so even "window
// = 90d" is cheap. Admins visualising heavier traffic should still paginate
// through row-level queries rather than ask for all-time here.
func (h *Handler) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	since, window, err := parseUsageWindow(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	summary, err := h.container.Store().GetUsageSummary(r.Context(), since)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, toSummaryView(summary, window))
}

func toSummaryView(s *store.UsageSummary, windowDays int) usageSummaryView {
	out := usageSummaryView{
		Since:      s.Since,
		WindowDays: windowDays,
	}
	for _, u := range s.Users {
		out.Users = append(out.Users, userUsageView{
			UserID:           u.UserID,
			UserName:         u.UserName,
			Requests:         u.Requests,
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			TotalTokens:      u.PromptTokens + u.CompletionTokens,
		})
	}
	for _, p := range s.Providers {
		out.Providers = append(out.Providers, providerUsageView{
			Provider:         p.Provider,
			Requests:         p.Requests,
			PromptTokens:     p.PromptTokens,
			CompletionTokens: p.CompletionTokens,
			TotalTokens:      p.PromptTokens + p.CompletionTokens,
		})
	}
	return out
}

// parseUsageWindow resolves either "since=" or "window=" into an absolute
// time lower-bound plus the implied window-in-days (for display).
func parseUsageWindow(q map[string][]string) (time.Time, int, error) {
	first := func(k string) string {
		if v, ok := q[k]; ok && len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
		return ""
	}

	if s := first("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}, 0, errors.New("since must be an RFC3339 timestamp")
		}
		days := int(time.Since(t).Hours() / 24)
		if days < 0 {
			days = 0
		}
		return t, days, nil
	}

	window := first("window")
	if window == "" {
		window = "7d"
	}
	// Accept "Nd" (days) as well as stdlib-parsable durations ("24h", "30m").
	if strings.HasSuffix(window, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(window, "d"))
		if err != nil || n < 0 {
			return time.Time{}, 0, errors.New("window must be a non-negative integer followed by 'd'")
		}
		return time.Now().Add(-time.Duration(n) * 24 * time.Hour), n, nil
	}
	d, err := time.ParseDuration(window)
	if err != nil || d < 0 {
		return time.Time{}, 0, errors.New("window must be like '7d' or a Go duration ('24h')")
	}
	return time.Now().Add(-d), int(d.Hours() / 24), nil
}
