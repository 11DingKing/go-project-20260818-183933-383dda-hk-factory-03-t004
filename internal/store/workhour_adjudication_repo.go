package store

import (
	"context"
	"database/sql"
	"fmt"

	"sitesync/internal/domain"
)

const workHourCols = `id, device_id, work_date, hours, reported_by, version, created_at, updated_at`

// UpsertWorkHour inserts or updates a workshop-reported work hour row.
func (s *Store) UpsertWorkHour(ctx context.Context, w domain.CustomerWorkHour) (domain.CustomerWorkHour, error) {
	w.Version = 1
	w.CreatedAt = s.clock.Now()
	w.UpdatedAt = w.CreatedAt
	_, err := s.txFrom(ctx).ExecContext(ctx, workHourUpsert, w.ID, w.DeviceID, w.WorkDate,
		decimalText(w.Hours), w.ReportedBy, w.Version, s.nowRFC3339(), s.nowRFC3339())
	if err != nil {
		return w, fmt.Errorf("store: upsert work hour: %w", err)
	}
	return w, nil
}

const workHourUpsert = `INSERT INTO customer_work_hours (id, device_id, work_date, hours, reported_by, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(device_id, work_date) DO UPDATE SET hours = excluded.hours, updated_at = excluded.updated_at, version = customer_work_hours.version + 1`

func scanWorkHour(sc rowScanner) (domain.CustomerWorkHour, error) {
	var w domain.CustomerWorkHour
	var hours, created, updated sql.NullString
	if err := sc.Scan(&w.ID, &w.DeviceID, &w.WorkDate, &hours, &w.ReportedBy, &w.Version, &created, &updated); err != nil {
		return w, err
	}
	w.Hours = parseDecimal(hours)
	w.CreatedAt = parseTime(created)
	w.UpdatedAt = parseTime(updated)
	return w, nil
}

// GetWorkHourByDeviceDate loads a work hour for a device and date.
func (s *Store) GetWorkHourByDeviceDate(ctx context.Context, deviceID, date string) (domain.CustomerWorkHour, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+workHourCols+` FROM customer_work_hours WHERE device_id = ? AND work_date = ?`, scanWorkHour, fmt.Sprintf("store: get work hour %s/%s", deviceID, date), deviceID, date)
}

// ListWorkHoursByDevice returns all reported hours for a device.
func (s *Store) ListWorkHoursByDevice(ctx context.Context, deviceID string) ([]domain.CustomerWorkHour, error) {
	return queryRows(ctx, s.txFrom(ctx), `SELECT `+workHourCols+` FROM customer_work_hours WHERE device_id = ? ORDER BY work_date`,
		"store: list work hours", scanWorkHour, deviceID)
}

const adjudicationCols = `id, record_id, work_hour_id, winner, delta_hours, attributed_to, adjudicator_id, reason, decided_at, version`

// CreateAdjudication inserts a ruling. One ruling per record (unique record_id).
func (s *Store) CreateAdjudication(ctx context.Context, a domain.Adjudication) (domain.Adjudication, error) {
	a.Version = 1
	res, err := s.txFrom(ctx).ExecContext(ctx, adjudicationInsert, a.ID, a.RecordID, a.WorkHourID, a.Winner,
		decimalText(a.DeltaHours), a.AttributedTo, a.AdjudicatorID, a.Reason, formatTime(a.DecidedAt), a.Version)
	return a, dupInsert(res, err, "adjudication", a.RecordID)
}

const adjudicationInsert = `INSERT INTO adjudications (id, record_id, work_hour_id, winner, delta_hours, attributed_to, adjudicator_id, reason, decided_at, version)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

func scanAdjudication(sc rowScanner) (domain.Adjudication, error) {
	var a domain.Adjudication
	var delta, decided sql.NullString
	if err := sc.Scan(&a.ID, &a.RecordID, &a.WorkHourID, &a.Winner, &delta, &a.AttributedTo, &a.AdjudicatorID, &a.Reason, &decided, &a.Version); err != nil {
		return a, err
	}
	a.DeltaHours = parseDecimal(delta)
	a.DecidedAt = parseTime(decided)
	return a, nil
}

// GetAdjudicationByRecord loads a ruling by record id.
func (s *Store) GetAdjudicationByRecord(ctx context.Context, recordID string) (domain.Adjudication, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+adjudicationCols+` FROM adjudications WHERE record_id = ?`, scanAdjudication, "store: get adjudication by record "+recordID, recordID)
}
