package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
)

const orderCols = `id, code, customer_id, field_engineer_id, customer_manager_id, trial_id, status,
handling_mode, responsible_role, backfill_window_hours, last_error, retry_count, version, created_at, updated_at`

const stepCols = `id, order_id, step_no, step_name, status, attempt_count, last_error, version, updated_at`

// CreateOrder inserts a deployment order and its five pending step rows in one
// transaction so the order is never left without its saga skeleton.
func (s *Store) CreateOrder(ctx context.Context, o domain.DeploymentOrder) (domain.DeploymentOrder, error) {
	ex := s.txFrom(ctx)
	now := s.nowRFC3339()
	o.Version = 1
	o.CreatedAt = s.clock.Now()
	o.UpdatedAt = o.CreatedAt
	if o.Status == "" {
		o.Status = domain.DeploymentDraft
	}
	if o.HandlingMode == "" {
		o.HandlingMode = domain.HandlingOnSiteDebug
	}
	if o.ResponsibleRole == "" {
		o.ResponsibleRole = domain.ResponsibleFieldEngineer
	}
	if o.BackfillWindowHours == 0 {
		o.BackfillWindowHours = 168
	}
	res, err := ex.ExecContext(ctx, orderInsert, o.ID, o.Code, o.CustomerID, o.FieldEngineerID,
		sql.NullString{String: o.CustomerManagerID, Valid: o.CustomerManagerID != ""},
		sql.NullString{String: o.TrialID, Valid: o.TrialID != ""},
		string(o.Status), string(o.HandlingMode), string(o.ResponsibleRole), o.BackfillWindowHours,
		sql.NullString{String: o.LastError, Valid: o.LastError != ""}, o.RetryCount, o.Version, now, now)
	if err := dupInsert(res, err, "order", o.Code); err != nil {
		return o, err
	}
	return o, s.initStepsInTx(ctx, o.ID)
}

const orderInsert = `INSERT INTO deployment_orders
(id, code, customer_id, field_engineer_id, customer_manager_id, trial_id, status, handling_mode,
 responsible_role, backfill_window_hours, last_error, retry_count, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// initStepsInTx creates the five canonical saga step rows for an order.
func (s *Store) initStepsInTx(ctx context.Context, orderID string) error {
	now := s.nowRFC3339()
	for i, name := range domain.OrderedSteps {
		id := stepID(orderID, i)
		if _, err := s.txFrom(ctx).ExecContext(ctx, stepInsert, id, orderID, i+1, name,
			string(domain.StepPending), 0, sql.NullString{}, 1, now); err != nil {
			return fmt.Errorf("store: init step %s: %w", name, err)
		}
	}
	return nil
}

const stepInsert = `INSERT INTO deployment_steps (id, order_id, step_no, step_name, status, attempt_count, last_error, version, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

func stepID(orderID string, idx int) string {
	return fmt.Sprintf("%s-step-%d", orderID, idx+1)
}

func scanOrder(sc rowScanner) (domain.DeploymentOrder, error) {
	var o domain.DeploymentOrder
	var status, handling, role string
	var cmID, trialID, lastErr, created, updated sql.NullString
	if err := sc.Scan(&o.ID, &o.Code, &o.CustomerID, &o.FieldEngineerID, &cmID, &trialID,
		&status, &handling, &role, &o.BackfillWindowHours, &lastErr, &o.RetryCount, &o.Version, &created, &updated); err != nil {
		return o, err
	}
	o.CustomerManagerID = cmID.String
	o.TrialID = trialID.String
	o.LastError = lastErr.String
	o.Status = domain.DeploymentStatus(status)
	o.HandlingMode = domain.HandlingMode(handling)
	o.ResponsibleRole = domain.ResponsibleRole(role)
	o.CreatedAt = parseTime(created)
	o.UpdatedAt = parseTime(updated)
	return o, nil
}

// GetOrder loads a deployment order by id.
func (s *Store) GetOrder(ctx context.Context, id string) (domain.DeploymentOrder, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+orderCols+` FROM deployment_orders WHERE id = ?`, scanOrder, "store: get order "+id, id)
}

// UpdateStatus transitions a deployment order with optimistic locking. The
// WHERE clause requires both the expected prior status and version; a mismatch
// yields a retryable version-conflict error.
func (s *Store) UpdateStatus(ctx context.Context, id string, from, to domain.DeploymentStatus, fromVersion int) error {
	return s.execVersioned(ctx, orderUpdateStatus, "store: update order status",
		string(to), s.nowRFC3339(), id, string(from), fromVersion)
}

const orderUpdateStatus = `UPDATE deployment_orders SET status = ?, updated_at = ?, version = version + 1
WHERE id = ? AND status = ? AND version = ?`

// UpdateResponsibility changes who owns a deployment (escalation) with optimistic locking.
func (s *Store) UpdateResponsibility(ctx context.Context, id string, role domain.ResponsibleRole, mode domain.HandlingMode, customerManagerID string, fromVersion int) error {
	return s.execVersioned(ctx, orderUpdateResp, "store: update responsibility",
		string(role), string(mode), sql.NullString{String: customerManagerID, Valid: customerManagerID != ""},
		s.nowRFC3339(), id, fromVersion)
}

const orderUpdateResp = `UPDATE deployment_orders SET responsible_role = ?, handling_mode = ?, customer_manager_id = ?, updated_at = ?, version = version + 1
WHERE id = ? AND version = ?`

// LinkTrial associates a trial id with its order under optimistic locking.
func (s *Store) LinkTrial(ctx context.Context, orderID, trialID string, fromVersion int) error {
	return s.execVersioned(ctx, `UPDATE deployment_orders SET trial_id = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ?`,
		"store: link trial", trialID, s.nowRFC3339(), orderID, fromVersion)
}

// BumpRetry records a provisioning failure and increments the retry counter.
func (s *Store) BumpRetry(ctx context.Context, id string, lastError string) error {
	return s.execVersioned(ctx, `UPDATE deployment_orders SET last_error = ?, retry_count = retry_count + 1, updated_at = ?, version = version + 1 WHERE id = ?`,
		"store: bump retry", lastError, s.nowRFC3339(), id)
}

// ListByStatus returns orders in any of the given statuses (used by recovery).
func (s *Store) ListByStatus(ctx context.Context, statuses ...domain.DeploymentStatus) ([]domain.DeploymentOrder, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, len(statuses))
	for i, st := range statuses {
		placeholders[i] = "?"
		args[i] = string(st)
	}
	q := `SELECT ` + orderCols + ` FROM deployment_orders WHERE status IN (` + strings.Join(placeholders, ",") + `) ORDER BY created_at`
	return queryRows(ctx, s.txFrom(ctx), q, "store: list orders by status", scanOrder, args...)
}

// checkOptimistic maps zero-affected to a retryable version-conflict error.
func checkOptimistic(n int64, wrap string) error {
	if n == 0 {
		return fmt.Errorf("%s: %w", wrap, errorsx.ErrVersionConflict)
	}
	return nil
}

func scanStep(sc rowScanner) (domain.DeploymentStep, error) {
	var st domain.DeploymentStep
	var status, lastErr, updated sql.NullString
	if err := sc.Scan(&st.ID, &st.OrderID, &st.StepNo, &st.StepName, &status, &st.AttemptCount, &lastErr, &st.Version, &updated); err != nil {
		return st, err
	}
	st.Status = domain.StepStatus(status.String)
	st.LastError = lastErr.String
	st.UpdatedAt = parseTime(updated)
	return st, nil
}

// ListStepsByOrder returns the saga steps for an order ordered by step_no.
func (s *Store) ListStepsByOrder(ctx context.Context, orderID string) ([]domain.DeploymentStep, error) {
	return queryRows(ctx, s.txFrom(ctx), `SELECT `+stepCols+` FROM deployment_steps WHERE order_id = ? ORDER BY step_no`,
		"store: list steps", scanStep, orderID)
}

// claimStep runs a versioned UPDATE then reloads the step by id. Used by
// MarkProcessing and MarkDone which return the updated step to the saga.
func (s *Store) claimStep(ctx context.Context, id, query, wrap string, args ...any) (domain.DeploymentStep, error) {
	res, err := s.txFrom(ctx).ExecContext(ctx, query, args...)
	if err != nil {
		return domain.DeploymentStep{}, fmt.Errorf("%s: %w", wrap, err)
	}
	n, _ := rowsAffected(res)
	if n == 0 {
		return domain.DeploymentStep{}, fmt.Errorf("%s: %w", wrap, errorsx.ErrVersionConflict)
	}
	return s.stepByRowID(ctx, id)
}

// stepByRowID loads a step by its row id.
func (s *Store) stepByRowID(ctx context.Context, id string) (domain.DeploymentStep, error) {
	return queryOne(ctx, s.txFrom(ctx), `SELECT `+stepCols+` FROM deployment_steps WHERE id = ?`, scanStep, "store: get step by id "+id, id)
}

// MarkProcessing atomically claims a pending/failed step into processing.
func (s *Store) MarkProcessing(ctx context.Context, id string, fromVersion int) (domain.DeploymentStep, error) {
	return s.claimStep(ctx, id, `UPDATE deployment_steps SET status = ?, attempt_count = attempt_count + 1, updated_at = ?, version = version + 1
WHERE id = ? AND version = ? AND status IN (?, ?)`,
		"store: mark processing", string(domain.StepProcessing), s.nowRFC3339(), id, fromVersion, string(domain.StepPending), string(domain.StepFailed))
}

// MarkDone completes a step with optimistic locking.
func (s *Store) MarkDone(ctx context.Context, id string, fromVersion int) (domain.DeploymentStep, error) {
	return s.claimStep(ctx, id, `UPDATE deployment_steps SET status = ?, last_error = '', updated_at = ?, version = version + 1 WHERE id = ? AND version = ?`,
		"store: mark done", string(domain.StepDone), s.nowRFC3339(), id, fromVersion)
}

// MarkFailed records a failure on a step with optimistic locking.
func (s *Store) MarkFailed(ctx context.Context, id string, fromVersion int, lastErr string) error {
	return s.execVersioned(ctx, `UPDATE deployment_steps SET status = ?, last_error = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ?`,
		"store: mark failed", string(domain.StepFailed), lastErr, s.nowRFC3339(), id, fromVersion)
}

// MarkCompensated reverses a done step idempotently.
func (s *Store) MarkCompensated(ctx context.Context, id string, fromVersion int) error {
	return s.execVersioned(ctx, `UPDATE deployment_steps SET status = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ?`,
		"store: mark compensated", string(domain.StepCompensated), s.nowRFC3339(), id, fromVersion)
}

// BindDevice links a device to an order idempotently.
func (s *Store) BindDevice(ctx context.Context, orderID, deviceID string) error {
	id := fmt.Sprintf("%s-dev-%s", orderID, deviceID)
	_, err := s.txFrom(ctx).ExecContext(ctx, `INSERT INTO deployment_devices (id, order_id, device_id, bound_at, version) VALUES (?, ?, ?, ?, 1)
ON CONFLICT(order_id, device_id) DO UPDATE SET version = version`, id, orderID, deviceID, s.nowRFC3339())
	if err != nil {
		return fmt.Errorf("store: bind device: %w", err)
	}
	return nil
}

// ListDevicesByOrder returns devices bound to an order.
func scanOrderDevice(sc rowScanner) (domain.DeploymentDevice, error) {
	var dd domain.DeploymentDevice
	var bound sql.NullString
	if err := sc.Scan(&dd.ID, &dd.OrderID, &dd.DeviceID, &bound, &dd.Version); err != nil {
		return dd, err
	}
	dd.BoundAt = parseTime(bound)
	return dd, nil
}

func (s *Store) ListDevicesByOrder(ctx context.Context, orderID string) ([]domain.DeploymentDevice, error) {
	return queryRows(ctx, s.txFrom(ctx), `SELECT id, order_id, device_id, bound_at, version FROM deployment_devices WHERE order_id = ? ORDER BY bound_at`,
		"store: list order devices", scanOrderDevice, orderID)
}

// UnbindAllDevices removes every device binding for an order (compensation).
func (s *Store) UnbindAllDevices(ctx context.Context, orderID string) (int64, error) {
	res, err := s.txFrom(ctx).ExecContext(ctx, `DELETE FROM deployment_devices WHERE order_id = ?`, orderID)
	if err != nil {
		return 0, fmt.Errorf("store: unbind devices: %w", err)
	}
	return rowsAffected(res)
}
