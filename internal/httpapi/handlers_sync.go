package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"sitesync/internal/domain"
)

func (s *Server) queryRecords(w http.ResponseWriter, r *http.Request) {
	f := domainRecordFilter(r)
	res, err := s.services.Query.QueryRecords(r.Context(), f, parsePage(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) pullChanges(w http.ResponseWriter, r *http.Request) {
	since := queryInt(r, "since", 0)
	limit := queryInt(r, "limit", 0)
	records, err := s.services.Sync.PullChanges(r.Context(), since, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	next := since
	for _, rec := range records {
		if rec.ChangeVersion > next {
			next = rec.ChangeVersion
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records, "next_version": next})
}

func (s *Server) manualVerify(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		ReviewerID string `json:"reviewer_id"`
		Decision   string `json:"decision"`
		Note       string `json:"note"`
	}
	if !s.decodeOrFail(w, r, &req) {
		return
	}
	manual, record, err := s.services.Sync.ManualVerify(r.Context(), id, req.ReviewerID, req.Decision, req.Note)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"manual": manual, "record": record})
}

func (s *Server) listAccumulated(w http.ResponseWriter, r *http.Request) {
	f := domain.SyncAccumulatedFilter{
		OrderID: queryString(r, "order_id"),
		Status:  domain.SyncBatchStatus(queryString(r, "status")),
	}
	res, err := s.services.Query.ListAccumulated(r.Context(), f, parsePage(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) listManualPending(w http.ResponseWriter, r *http.Request) {
	res, err := s.services.Query.ListManualPending(r.Context(), parsePage(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) listStaleManuals(w http.ResponseWriter, r *http.Request) {
	older := queryInt(r, "older_than_hours", 0)
	res, err := s.services.Query.ListStaleManuals(r.Context(), older, parsePage(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
