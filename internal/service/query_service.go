package service

import (
	"context"

	"sitesync/internal/domain"
	"sitesync/internal/window"
)

// QueryService is the read side: filtered, paginated listings of records, audit,
// accumulated batches, manual reviews and dead letters.
type QueryService struct {
	deps Deps
}

// pagedQuery normalizes the page, runs list and assembles a Result so every
// read-side query shares one boundary rule.
func pagedQuery[T any](page domain.Page, list func(domain.Page) ([]T, int64, error)) (domain.Result[T], error) {
	page = page.Normalize()
	items, total, err := list(page)
	if err != nil {
		return domain.Result[T]{}, err
	}
	return pageResult(items, total, page), nil
}

// QueryRecords returns a page of operation records matching the filter.
func (s *QueryService) QueryRecords(ctx context.Context, f domain.RecordFilter, page domain.Page) (domain.Result[domain.OperationRecord], error) {
	return pagedQuery(page, func(pg domain.Page) ([]domain.OperationRecord, int64, error) {
		return s.deps.Records.ListRecordsByFilter(ctx, f, pg)
	})
}

// QueryAudit returns a page of audit entries matching the filter.
func (s *QueryService) QueryAudit(ctx context.Context, f domain.AuditFilter, page domain.Page) (domain.Result[domain.AuditEntry], error) {
	return pagedQuery(page, func(pg domain.Page) ([]domain.AuditEntry, int64, error) {
		return s.deps.Audit.ListAuditByFilter(ctx, f, pg)
	})
}

// ListAccumulated returns sync batches not yet completed (the offline backlog).
func (s *QueryService) ListAccumulated(ctx context.Context, f domain.SyncAccumulatedFilter, page domain.Page) (domain.Result[domain.SyncBatch], error) {
	return pagedQuery(page, func(pg domain.Page) ([]domain.SyncBatch, int64, error) {
		return s.deps.Batches.ListAccumulatedBatches(ctx, f, pg)
	})
}

// ListManualPending returns records awaiting manual verification.
func (s *QueryService) ListManualPending(ctx context.Context, page domain.Page) (domain.Result[domain.ManualVerification], error) {
	return pagedQuery(page, func(pg domain.Page) ([]domain.ManualVerification, int64, error) {
		return s.deps.Manuals.ListManualPending(ctx, pg)
	})
}

// ListPermanentFailures returns dead-letter entries.
func (s *QueryService) ListPermanentFailures(ctx context.Context, page domain.Page) (domain.Result[domain.PermanentFailure], error) {
	return pagedQuery(page, func(pg domain.Page) ([]domain.PermanentFailure, int64, error) {
		return s.deps.Failures.ListFailures(ctx, pg)
	})
}

// RequeueFailure re-arms a dead-letter entry for retry.
func (s *QueryService) RequeueFailure(ctx context.Context, id string) error {
	if err := s.deps.Failures.RequeueFailure(ctx, id); err != nil {
		return err
	}
	s.deps.audit(ctx, "ops_specialist", "ops_specialist", "failure.requeued", "permanent_failure", id, "")
	return nil
}

// ListStaleManuals returns manual-verification rows whose review window has
// closed and which must be escalated to a higher tier for human attention. The
// window is the caller-supplied olderThanHours when positive, otherwise the
// configured Backfill.ManualReviewAfterHours. Because staleness is a derived
// attribute, pending rows are scanned in full so the classification is exact,
// then the requested page is applied to the stale subset.
func (s *QueryService) ListStaleManuals(ctx context.Context, olderThanHours int, page domain.Page) (domain.Result[domain.ManualVerification], error) {
	page = page.Normalize()
	windowHours := olderThanHours
	if windowHours <= 0 {
		windowHours = s.deps.Cfg.Backfill.ManualReviewAfterHours
	}
	policy := window.EscalationPolicy{}
	now := s.deps.now()
	var stale []domain.ManualVerification
	scan := domain.Page{Size: domain.MaxPageSize}
	for {
		items, total, err := s.deps.Manuals.ListManualPending(ctx, scan)
		if err != nil {
			return domain.Result[domain.ManualVerification]{}, err
		}
		for _, m := range items {
			if policy.Stale(m.CreatedAt, windowHours, now) {
				stale = append(stale, m)
			}
		}
		if len(items) < scan.Size || int64(scan.Offset+len(items)) >= total {
			break
		}
		scan.Offset += len(items)
	}
	total := int64(len(stale))
	start, end := page.Offset, page.Offset+page.Size
	if start > len(stale) {
		start = len(stale)
	}
	if end > len(stale) {
		end = len(stale)
	}
	pageItems := stale[start:end]
	if pageItems == nil {
		pageItems = []domain.ManualVerification{}
	}
	return domain.Result[domain.ManualVerification]{
		Items: pageItems, Offset: page.Offset, Size: len(pageItems), Total: total,
		Next: nextPage(page.Offset, len(pageItems), total),
	}, nil
}

// pageResult assembles a domain.Result from a repository page response so every
// paginated query shares one shape and one boundary rule.
func pageResult[T any](items []T, total int64, page domain.Page) domain.Result[T] {
	return domain.Result[T]{
		Items: items, Offset: page.Offset, Size: len(items), Total: total,
		Next: nextPage(page.Offset, len(items), total),
	}
}

// nextPage returns the next offset when more rows exist, else 0.
func nextPage(offset, returned int, total int64) int {
	if int64(offset+returned) >= total {
		return 0
	}
	return offset + returned
}
