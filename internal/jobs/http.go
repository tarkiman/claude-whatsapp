package jobs

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// Handler serves the job API (all routes need "Authorization: Bearer <token>"):
//
//	GET  /projects               allowed projects, whether each has a session, any running job
//	POST /jobs                   {"project", "instruction", "new_session"} -> 202 job | 404 | 409 {job in the way}
//	GET  /jobs?project=&limit=   recent jobs, newest first
//	GET  /jobs/{id}              one job (status, activity, steps, summary, detail)
//	POST /jobs/{id}/cancel       stop a running job
func Handler(m *Manager, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /projects", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"projects": m.Projects()})
	})
	mux.HandleFunc("POST /jobs", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Project     string `json:"project"`
			Instruction string `json:"instruction"`
			NewSession  bool   `json:"new_session"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected JSON {project, instruction, new_session}"})
			return
		}
		job, err := m.Start(req.Project, req.Instruction, req.NewSession)
		var busy *BusyError
		switch {
		case errors.As(err, &busy):
			writeJSON(w, http.StatusConflict, map[string]any{"error": "busy", "job": busy.Job})
		case errors.Is(err, ErrUnknownProject):
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown project", "projects": m.Projects()})
		case errors.Is(err, ErrEmpty):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty instruction"})
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusAccepted, job)
		}
	})
	mux.HandleFunc("GET /jobs", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 10
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": m.List(r.URL.Query().Get("project"), limit)})
	})
	mux.HandleFunc("GET /jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		job, err := m.Get(r.PathValue("id"))
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, job)
	})
	mux.HandleFunc("POST /jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		job, err := m.Cancel(r.PathValue("id"))
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, job)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Bearer " + token
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
