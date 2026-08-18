package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
)

// AdjudicationService decides field-vs-customer work-hour conflicts.
type AdjudicationService struct {
	deps Deps
}

// AdjudicateRequest is the wire input for a ruling.
type AdjudicateRequest struct {
	RecordID      string `json:"record_id"`
	Winner        string `json:"winner"`
	AdjudicatorID string `json:"adjudicator_id"`
	Reason        string `json:"reason"`
}

// Adjudicate records a ruling for a conflicting record. The party that loses
// bears the absolute hour delta. Repeated calls for the same record return the
// existing ruling without re-applying effects.
func (s *AdjudicationService) Adjudicate(ctx context.Context, req AdjudicateRequest) (domain.Adjudication, error) {
	if req.RecordID == "" || (req.Winner != domain.WinnerField && req.Winner != domain.WinnerCustomer) {
		return domain.Adjudication{}, fmt.Errorf("%w: record_id and winner (field|customer) are required", errorsx.ErrValidation)
	}
	record, err := s.deps.Records.GetRecord(ctx, req.RecordID)
	if err != nil {
		return domain.Adjudication{}, notFound("record", req.RecordID)
	}
	if record.Status == domain.RecordAdjudicated {
		if existing, err := s.deps.Adjudications.GetAdjudicationByRecord(ctx, req.RecordID); err == nil {
			return existing, nil
		}
	}
	if record.Status != domain.RecordConflict {
		return domain.Adjudication{}, fmt.Errorf("%w: record %s is %s, not conflict", errorsx.ErrIllegalTransition, req.RecordID, record.Status)
	}
	if record.ConflictID == "" {
		return domain.Adjudication{}, fmt.Errorf("%w: record %s has no linked work hour", errorsx.ErrIncomplete, req.RecordID)
	}
	wh, err := s.deps.WorkHours.GetWorkHourByDeviceDate(ctx, record.DeviceID, record.OccurredAt.UTC().Format("2006-01-02"))
	if err != nil {
		return domain.Adjudication{}, fmt.Errorf("adjudicate: load work hour: %w", err)
	}
	delta := record.Hours.Sub(wh.Hours).Abs()
	attributedTo := domain.WinnerCustomer
	if req.Winner == domain.WinnerCustomer {
		attributedTo = domain.WinnerField
	}
	adj := domain.Adjudication{
		ID: uuid.NewString(), RecordID: req.RecordID, WorkHourID: wh.ID, Winner: req.Winner,
		DeltaHours: delta, AttributedTo: attributedTo, AdjudicatorID: req.AdjudicatorID,
		Reason: req.Reason, DecidedAt: s.deps.now(),
	}
	err = s.deps.UOW.InTx(ctx, func(ctx context.Context) error {
		created, err := s.deps.Adjudications.CreateAdjudication(ctx, adj)
		if err != nil {
			return err
		}
		adj = created
		if err := s.deps.Records.UpdateRecordStatus(ctx, req.RecordID, domain.RecordConflict, domain.RecordAdjudicated, record.Version); err != nil {
			return err
		}
		s.deps.audit(ctx, req.AdjudicatorID, "adjudicator", "adjudication.create", "adjudication", created.ID,
			fmt.Sprintf("winner=%s delta=%s attributed=%s", req.Winner, delta.String(), attributedTo))
		return nil
	})
	if err != nil {
		return domain.Adjudication{}, err
	}
	return adj, nil
}
