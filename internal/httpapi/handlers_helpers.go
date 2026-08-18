package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"sitesync/internal/domain"
)

func parsePage(r *http.Request) domain.Page {
	return domain.Page{Offset: queryInt(r, "offset", 0), Size: queryInt(r, "size", 0)}
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func queryTime(r *http.Request, key string) time.Time {
	v := r.URL.Query().Get(key)
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

func queryString(r *http.Request, key string) string {
	return r.URL.Query().Get(key)
}

func domainRecordFilter(r *http.Request) domain.RecordFilter {
	return domain.RecordFilter{
		CustomerID: queryString(r, "customer_id"),
		DeviceID:   queryString(r, "device_id"),
		OrderID:    queryString(r, "order_id"),
		Status:     domain.RecordStatus(queryString(r, "status")),
		Source:     queryString(r, "source"),
		From:       queryTime(r, "from"),
		To:         queryTime(r, "to"),
	}
}

// decodeOrFail decodes the JSON body into dst or writes a 400 and returns false
// so every mutating handler shares one decode-or-fail path.
func (s *Server) decodeOrFail(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeErr(w, errStr("invalid request body: "+err.Error()))
		return false
	}
	return true
}
