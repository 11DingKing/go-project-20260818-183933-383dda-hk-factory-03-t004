package httpapi

import (
	"net/http"

	"sitesync/internal/service"
)

func (s *Server) createCustomer(w http.ResponseWriter, r *http.Request) {
	var req service.CreateCustomerRequest
	if !s.decodeOrFail(w, r, &req) {
		return
	}
	c, err := s.services.Registry.CreateCustomer(r.Context(), actorFrom(r), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) createDevice(w http.ResponseWriter, r *http.Request) {
	var req service.CreateDeviceRequest
	if !s.decodeOrFail(w, r, &req) {
		return
	}
	d, err := s.services.Registry.CreateDevice(r.Context(), actorFrom(r), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) createPerson(w http.ResponseWriter, r *http.Request) {
	var req service.CreatePersonRequest
	if !s.decodeOrFail(w, r, &req) {
		return
	}
	p, err := s.services.Registry.CreatePerson(r.Context(), actorFrom(r), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}
