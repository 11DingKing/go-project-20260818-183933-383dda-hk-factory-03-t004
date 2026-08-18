package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"sitesync/internal/domain"
	"sitesync/internal/service"
)

func (s *Server) adjudicate(w http.ResponseWriter, r *http.Request) {
	var req service.AdjudicateRequest
	if !s.decodeOrFail(w, r, &req) {
		return
	}
	adj, err := s.services.Adjudication.Adjudicate(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, adj)
}

func (s *Server) acceptTrial(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	trial, err := s.services.Trial.Accept(r.Context(), id, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, trial)
}

func (s *Server) convertTrial(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	trial, order, err := s.services.Trial.Convert(r.Context(), id, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trial": trial, "order": order})
}

func (s *Server) listOverdueTrials(w http.ResponseWriter, r *http.Request) {
	trials, err := s.services.Trial.ListOverdue(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": trials})
}

func (s *Server) generateBill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrderID  string          `json:"order_id"`
		PeriodNo int             `json:"period_no"`
		Rate     decimal.Decimal `json:"rate"`
	}
	if !s.decodeOrFail(w, r, &req) {
		return
	}
	bill, err := s.services.Reconciliation.GenerateBill(r.Context(), req.OrderID, req.PeriodNo, req.Rate, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bill)
}

func (s *Server) issueBill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	bill, err := s.services.Reconciliation.IssueBill(r.Context(), id, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bill)
}

func (s *Server) exportReconciliation(w http.ResponseWriter, r *http.Request) {
	f := domainRecordFilter(r)
	rows, total, err := s.services.Reconciliation.Export(r.Context(), f, parsePage(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "total": total})
}

func (s *Server) correctRecord(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Hours decimal.Decimal `json:"hours"`
	}
	if !s.decodeOrFail(w, r, &req) {
		return
	}
	rec, err := s.services.Reconciliation.CorrectRecord(r.Context(), id, req.Hours, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) revokeRecord(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Reason string `json:"reason"`
	}
	if !s.decodeOrFail(w, r, &req) {
		return
	}
	rec, err := s.services.Reconciliation.RevokeRecord(r.Context(), id, req.Reason, actorFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) reconcileDiff(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrderID string    `json:"order_id"`
		From    time.Time `json:"from"`
		To      time.Time `json:"to"`
	}
	if !s.decodeOrFail(w, r, &req) {
		return
	}
	diff, err := s.services.Reconciliation.ComputeDiff(r.Context(), req.OrderID, req.From, req.To)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, diff)
}

func (s *Server) queryAudit(w http.ResponseWriter, r *http.Request) {
	f := domain.AuditFilter{
		ActorID:    queryString(r, "actor_id"),
		EntityType: queryString(r, "entity_type"),
		EntityID:   queryString(r, "entity_id"),
		Action:     queryString(r, "action"),
		From:       queryTime(r, "from"),
		To:         queryTime(r, "to"),
	}
	res, err := s.services.Query.QueryAudit(r.Context(), f, parsePage(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) listPermanentFailures(w http.ResponseWriter, r *http.Request) {
	res, err := s.services.Query.ListPermanentFailures(r.Context(), parsePage(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) requeueFailure(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.services.Query.RequeueFailure(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "requeued"})
}
