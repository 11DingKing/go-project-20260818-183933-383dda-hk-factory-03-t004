package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
	"sitesync/internal/window"
)

// SyncService drives offline backfill, shift-poll batch processing, incremental
// pull and the manual-verification channel.
type SyncService struct {
	deps Deps
}

// BackfillInput is one offline record submitted for replay.
type BackfillInput struct {
	DeviceID       string          `json:"device_id"`
	ResponsibleID  string          `json:"responsible_id"`
	OccurredAt     time.Time       `json:"occurred_at"`
	ClientSequence int             `json:"client_sequence"`
	Hours          decimal.Decimal `json:"hours"`
	Content        string          `json:"content"`
}

// BackfillResult summarises a backfill submission.
type BackfillResult struct {
	BatchID string                   `json:"batch_id"`
	Pending int                      `json:"pending"`
	Records []domain.OperationRecord `json:"records"`
}

// Backfill accepts a batch of offline records and replays them in chronological
// order, allocating monotonic change versions and a pending sync batch. Records
// stay pending until the shift poller processes them.
func (s *SyncService) Backfill(ctx context.Context, orderID, actor string, inputs []BackfillInput) (BackfillResult, error) {
	if len(inputs) == 0 {
		return BackfillResult{}, fmt.Errorf("%w: backfill requires at least one record", errorsx.ErrValidation)
	}
	order, err := s.deps.Orders.GetOrder(ctx, orderID)
	if err != nil {
		return BackfillResult{}, notFound("deployment order", orderID)
	}
	if order.Status == domain.DeploymentAborted {
		return BackfillResult{}, fmt.Errorf("%w: order %s is aborted", errorsx.ErrIllegalTransition, orderID)
	}
	now := s.deps.now()
	sorted := make([]BackfillInput, len(inputs))
	copy(sorted, inputs)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].OccurredAt.Equal(sorted[j].OccurredAt) {
			return sorted[i].OccurredAt.Before(sorted[j].OccurredAt)
		}
		return sorted[i].ClientSequence < sorted[j].ClientSequence
	})
	for i, in := range sorted {
		if in.DeviceID == "" || in.ResponsibleID == "" || in.ClientSequence <= 0 {
			return BackfillResult{}, fmt.Errorf("%w: record %d missing device, responsible or sequence", errorsx.ErrValidation, i)
		}
		if in.OccurredAt.IsZero() {
			return BackfillResult{}, fmt.Errorf("%w: record %d missing occurred_at", errorsx.ErrValidation, i)
		}
	}
	batchID := uuid.NewString()
	records := make([]domain.OperationRecord, len(sorted))
	for i, in := range sorted {
		received := now
		records[i] = domain.OperationRecord{
			ID: uuid.NewString(), OrderID: orderID, DeviceID: in.DeviceID, ResponsibleID: in.ResponsibleID,
			OccurredAt: in.OccurredAt, RecordedAt: in.OccurredAt, ReceivedAt: &received,
			Source: domain.SourceBackfill, ClientSequence: in.ClientSequence, Hours: in.Hours,
			Content: in.Content, Status: domain.RecordPending, BatchID: batchID,
		}
	}
	allExisted := true
	err = s.deps.UOW.InTx(ctx, func(ctx context.Context) error {
		// Idempotency: if every sequence already exists, replay returns the existing
		// records without creating a new batch.
		allExist := true
		for i, in := range sorted {
			existing, err := s.deps.Records.GetByOrderSeq(ctx, orderID, in.ClientSequence)
			if err == nil {
				records[i] = existing
			} else {
				allExist = false
				allExisted = false
			}
		}
		if allExist {
			return nil
		}
		batch := domain.SyncBatch{ID: batchID, OrderID: orderID, Status: domain.SyncBatchPending, RecordCount: len(records)}
		if _, err := s.deps.Batches.CreateSyncBatch(ctx, batch); err != nil {
			return err
		}
		inserted, err := s.deps.Records.InsertBatch(ctx, records)
		if err != nil {
			return err
		}
		records = inserted
		last := 0
		for _, r := range inserted {
			if r.ChangeVersion > last {
				last = r.ChangeVersion
			}
		}
		if err := s.deps.SyncState.UpsertBackfill(ctx, orderID, now, last); err != nil {
			return err
		}
		s.deps.audit(ctx, actor, "field_engineer", "record.backfill", "sync_batch", batchID,
			fmt.Sprintf("order=%s count=%d", orderID, len(inserted)))
		return nil
	})
	if err != nil {
		return BackfillResult{}, err
	}
	result := BackfillResult{Records: records}
	if !allExisted {
		result.Pending = len(records)
		result.BatchID = batchID
	} else if len(records) > 0 {
		result.BatchID = records[0].BatchID
	}
	return result, nil
}

// ProcessBatch acquires a lease and resolves every pending record in a batch:
// expired-window records route to manual verification, conflicting records are
// flagged for adjudication, the rest are accepted.
func (s *SyncService) ProcessBatch(ctx context.Context, batchID, owner string) (domain.SyncBatch, int, error) {
	leaseUntil := s.deps.now().Add(s.deps.Cfg.Scheduler.LeaseTTL)
	batch, err := s.deps.Batches.AcquireLease(ctx, batchID, owner, leaseUntil)
	if err != nil {
		return batch, 0, err
	}
	records, err := s.deps.Records.ListRecordsByBatch(ctx, batchID)
	if err != nil {
		_, _ = s.deps.Batches.ReleaseLease(ctx, batchID)
		return batch, 0, fmt.Errorf("list batch records: %w", err)
	}
	var processed int
	perr := s.deps.UOW.InTx(ctx, func(ctx context.Context) error {
		for _, r := range records {
			if err := s.resolveRecord(ctx, r, batch.OrderID); err != nil {
				return err
			}
			processed++
		}
		return nil
	})
	if perr != nil {
		_, _ = s.deps.Batches.ReleaseLease(ctx, batchID)
		return batch, 0, perr
	}
	if err := s.deps.Batches.UpdateBatchProgress(ctx, batchID, processed, "", domain.SyncBatchCompleted, batch.Version); err != nil {
		return batch, processed, err
	}
	final, _ := s.deps.Batches.GetSyncBatch(ctx, batchID)
	s.deps.audit(ctx, owner, "scheduler", "batch.processed", "sync_batch", batchID,
		fmt.Sprintf("processed=%d", processed))
	return final, processed, nil
}

// resolveRecord applies the window and conflict rules to one pending record.
func (s *SyncService) resolveRecord(ctx context.Context, r domain.OperationRecord, orderID string) error {
	if r.Status != domain.RecordPending {
		return nil
	}
	order, err := s.deps.Orders.GetOrder(ctx, r.OrderID)
	if err != nil {
		return fmt.Errorf("resolve: load order: %w", err)
	}
	now := s.deps.now()
	verdict, reason := window.BackfillPolicy{}.Classify(r.OccurredAt, order.BackfillWindowHours, now)
	if verdict == window.ExpiredManual {
		return s.routeToManual(ctx, r, reason)
	}
	date := r.OccurredAt.UTC().Format("2006-01-02")
	wh, err := s.deps.WorkHours.GetWorkHourByDeviceDate(ctx, r.DeviceID, date)
	if err == nil && wh.ID != "" {
		if !wh.Hours.Equal(r.Hours) {
			if err := s.deps.Records.SetConflict(ctx, r.ID, wh.ID, r.Version); err != nil {
				return fmt.Errorf("resolve: set conflict: %w", err)
			}
			s.deps.audit(ctx, "scheduler", "scheduler", "record.conflict", "operation_record", r.ID,
				fmt.Sprintf("field=%s customer=%s", r.Hours.String(), wh.Hours.String()))
			return nil
		}
	}
	if err := s.deps.Records.UpdateRecordStatus(ctx, r.ID, domain.RecordPending, domain.RecordAccepted, r.Version); err != nil {
		return fmt.Errorf("resolve: accept: %w", err)
	}
	return nil
}

// routeToManual creates a manual-verification row and routes the record to it.
func (s *SyncService) routeToManual(ctx context.Context, r domain.OperationRecord, reason string) error {
	existing, err := s.deps.Manuals.GetManualByRecord(ctx, r.ID)
	if err == nil && existing.ID != "" {
		return s.deps.Records.SetManual(ctx, r.ID, existing.ID, r.Version)
	}
	manual := domain.ManualVerification{
		ID: uuid.NewString(), RecordID: r.ID, OrderID: r.OrderID, Reason: reason, Status: domain.ManualPending,
	}
	created, err := s.deps.Manuals.CreateManualVerification(ctx, manual)
	if err != nil {
		return fmt.Errorf("route manual: %w", err)
	}
	return s.deps.Records.SetManual(ctx, r.ID, created.ID, r.Version)
}

// PullChanges returns records with change_version greater than sinceVersion, in
// ascending order. Clients persist the highest version seen and pass it back.
func (s *SyncService) PullChanges(ctx context.Context, sinceVersion, limit int) ([]domain.OperationRecord, error) {
	if limit <= 0 || limit > domain.MaxPageSize {
		limit = domain.DefaultPageSize
	}
	return s.deps.Records.ListRecordChanges(ctx, sinceVersion, limit)
}

// ManualVerify reviews a record routed to the manual channel. Accept marks the
// record verified; reject revokes it. The review is idempotent on repeat calls.
func (s *SyncService) ManualVerify(ctx context.Context, recordID, reviewerID, decision, note string) (domain.ManualVerification, domain.OperationRecord, error) {
	if decision != domain.DecisionAccept && decision != domain.DecisionReject {
		return domain.ManualVerification{}, domain.OperationRecord{}, fmt.Errorf("%w: decision must be accept or reject", errorsx.ErrValidation)
	}
	record, err := s.deps.Records.GetRecord(ctx, recordID)
	if err != nil {
		return domain.ManualVerification{}, domain.OperationRecord{}, notFound("record", recordID)
	}
	manual, err := s.deps.Manuals.GetManualByRecord(ctx, recordID)
	if err != nil {
		return manual, record, notFound("manual verification", recordID)
	}
	if record.Status != domain.RecordManualVerifyNeeded {
		if (record.Status == domain.RecordVerified || record.Status == domain.RecordRevoked) && manual.Status == domain.ManualReviewed {
			return manual, record, nil
		}
		return manual, record, fmt.Errorf("%w: record %s is %s, not manual_verify_pending", errorsx.ErrIllegalTransition, recordID, record.Status)
	}
	if manual.Status == domain.ManualReviewed {
		return manual, record, nil
	}
	reviewed, err := s.deps.Manuals.ReviewManual(ctx, manual.ID, reviewerID, s.deps.now(), decision, note, manual.Version)
	if err != nil {
		return manual, record, err
	}
	target := domain.RecordVerified
	if decision == domain.DecisionReject {
		target = domain.RecordRevoked
	}
	if err := s.deps.Records.UpdateRecordStatus(ctx, record.ID, domain.RecordManualVerifyNeeded, target, record.Version); err != nil {
		return reviewed, record, err
	}
	s.deps.audit(ctx, reviewerID, "ops_specialist", "record.manual_reviewed", "operation_record", recordID, "decision="+decision)
	final, _ := s.deps.Records.GetRecord(ctx, recordID)
	return reviewed, final, nil
}

// ReclaimExpiredLeases reclaims sync batches whose leases have expired.
func (s *SyncService) ReclaimExpiredLeases(ctx context.Context) (int64, error) {
	return s.deps.Batches.ReclaimExpired(ctx, s.deps.now())
}
