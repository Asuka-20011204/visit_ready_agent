package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"visitready/internal/session"
)

const readinessTimeout = 2 * time.Second

type storePinger interface {
	Ready(context.Context) error
}

type healthResponse struct {
	Status               string            `json:"status"`
	Components           map[string]string `json:"components,omitempty"`
	AcceptingNewSessions *bool             `json:"accepting_new_sessions,omitempty"`
	RequestID            string            `json:"request_id"`
}

func (h *handler) liveness(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet, requestID)
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "alive", RequestID: requestID})
}

func (h *handler) readiness(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet, requestID)
		return
	}
	response := healthResponse{
		Status:               "ready",
		Components:           map[string]string{"session_store": "ok"},
		AcceptingNewSessions: boolPointer(true),
		RequestID:            requestID,
	}
	pinger, ok := h.store.(storePinger)
	if !ok {
		response.Status = "not_ready"
		response.Components["session_store"] = "unavailable"
		writeJSON(w, http.StatusServiceUnavailable, response)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()
	if err := pinger.Ready(ctx); err != nil {
		if errors.Is(err, session.ErrCapacity) {
			response.Components["session_store"] = "at_capacity"
			response.AcceptingNewSessions = boolPointer(false)
			writeJSON(w, http.StatusOK, response)
			return
		}
		response.Status = "not_ready"
		response.Components["session_store"] = "unavailable"
		response.AcceptingNewSessions = boolPointer(false)
		writeJSON(w, http.StatusServiceUnavailable, response)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func boolPointer(value bool) *bool { return &value }
