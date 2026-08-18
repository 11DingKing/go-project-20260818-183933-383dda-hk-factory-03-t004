package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
)

// healthz reports only that the process is alive.
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz probes every necessary dependency and reports 503 with the failing
// check when any one is not ready.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	type check struct {
		name string
		ok   bool
		err  string
	}
	var failed []check

	if err := s.probes.DBPing(ctx); err != nil {
		failed = append(failed, check{"database", false, err.Error()})
	}
	if dir := s.probes.DataDir; dir != "" {
		if err := dirWritable(dir); err != nil {
			failed = append(failed, check{"data_dir", false, err.Error()})
		}
	}
	if !s.probes.SchedulerReady() {
		failed = append(failed, check{"scheduler", false, "not started"})
	}
	if v, err := s.probes.SchemaVersion(ctx); err != nil || v == 0 {
		failed = append(failed, check{"migrations", false, "schema not applied"})
	}

	if len(failed) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not ready", "failures": failed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// dirWritable writes a probe file into the data directory to confirm it is usable.
func dirWritable(dir string) error {
	probe := filepath.Join(dir, ".readyz-probe")
	f, err := os.Create(probe)
	if err != nil {
		return err
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return nil
}
