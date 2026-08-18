package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"sitesync/internal/service"
)

func (s *Server) createDeployment(w http.ResponseWriter, r *http.Request) {
	var req service.CreateOrderRequest
	if !s.decodeOrFail(w, r, &req) {
		return
	}
	order, err := s.services.Deployment.CreateOrder(r.Context(), actorFrom(r), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, order)
}

func (s *Server) provisionDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	order, steps, err := s.services.Deployment.Provision(r.Context(), id, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"order": order, "steps": steps})
}

func (s *Server) compensateDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	order, steps, err := s.services.Deployment.Compensate(r.Context(), id, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"order": order, "steps": steps})
}

func (s *Server) getDeployment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	detail, err := s.services.Deployment.GetDetail(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) backfillRecords(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Records []service.BackfillInput `json:"records"`
	}
	if !s.decodeOrFail(w, r, &req) {
		return
	}
	res, err := s.services.Sync.Backfill(r.Context(), id, actorFrom(r), req.Records)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}
