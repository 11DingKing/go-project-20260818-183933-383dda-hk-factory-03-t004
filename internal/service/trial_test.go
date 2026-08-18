package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
)

// trialOf returns the active trial created during provisioning.
func trialOf(t *testing.T, e *env, orderID string) domain.Trial {
	t.Helper()
	order, err := e.deps.Orders.GetOrder(e.ctx(), orderID)
	require.NoError(t, err)
	require.NotEmpty(t, order.TrialID, "provisioning must link a trial to the order")
	trial, err := e.deps.Trials.GetTrial(e.ctx(), order.TrialID)
	require.NoError(t, err)
	return trial
}

func TestTrialAcceptSucceedsFromActive(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	trial := trialOf(t, e, orderID)
	assert.Equal(t, domain.TrialActive, trial.Status)

	accepted, err := e.services.Trial.Accept(e.ctx(), trial.ID, md.managerID)
	require.NoError(t, err)
	assert.Equal(t, domain.TrialAccepted, accepted.Status)
	require.NotNil(t, accepted.AcceptedAt)
	assert.Equal(t, md.managerID, accepted.AcceptedBy)
}

func TestTrialAcceptIsIdempotentDuplicate(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	trial := trialOf(t, e, orderID)

	first, err := e.services.Trial.Accept(e.ctx(), trial.ID, md.managerID)
	require.NoError(t, err)
	again, err := e.services.Trial.Accept(e.ctx(), trial.ID, md.managerID)
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, domain.TrialAccepted, again.Status)
}

func TestTrialEscalateAfterDeadlineTransfersResponsibility(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	trial := trialOf(t, e, orderID)
	assert.Equal(t, domain.TrialActive, trial.Status)

	// Push the clock past the acceptance deadline window.
	e.clk.Advance(time.Duration(e.deps.Cfg.Trial.AcceptanceWindowHours+1) * time.Hour)

	escalated, order, _, err := e.services.Trial.Escalate(e.ctx(), trial.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.TrialEscalated, escalated.Status)
	assert.Equal(t, domain.ResponsibleCustomerMgr, order.ResponsibleRole)
	assert.Equal(t, domain.HandlingUrgeAccept, order.HandlingMode)
}

func TestCheckDeadlinesEscalatesOverdueTrials(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	trial := trialOf(t, e, orderID)

	// Before the deadline, nothing escalates.
	count, err := e.services.Trial.CheckDeadlines(e.ctx())
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	e.clk.Advance(time.Duration(e.deps.Cfg.Trial.AcceptanceWindowHours+1) * time.Hour)

	count, err = e.services.Trial.CheckDeadlines(e.ctx())
	require.NoError(t, err)
	assert.Equal(t, 1, count, "one trial should be escalated past deadline")

	final, _ := e.deps.Trials.GetTrial(e.ctx(), trial.ID)
	assert.Equal(t, domain.TrialEscalated, final.Status)
	order, _ := e.deps.Orders.GetOrder(e.ctx(), orderID)
	assert.Equal(t, domain.ResponsibleCustomerMgr, order.ResponsibleRole)
}

func TestCheckDeadlinesIsIdempotentRetry(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	provisionedOrder(t, e, md)
	e.clk.Advance(time.Duration(e.deps.Cfg.Trial.AcceptanceWindowHours+1) * time.Hour)

	first, err := e.services.Trial.CheckDeadlines(e.ctx())
	require.NoError(t, err)
	assert.Equal(t, 1, first)
	second, err := e.services.Trial.CheckDeadlines(e.ctx())
	require.NoError(t, err)
	assert.Equal(t, 0, second, "already-escalated trial must not be re-escalated")
}

func TestIllegalTrialTransitionRejectsConvertFromActive(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	trial := trialOf(t, e, orderID)
	assert.Equal(t, domain.TrialActive, trial.Status)

	_, _, err := e.services.Trial.Convert(e.ctx(), trial.ID, md.managerID)
	assert.ErrorIs(t, err, errorsx.ErrIllegalTransition)
}

func TestTrialConvertFromEscalatedIsIdempotent(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	trial := trialOf(t, e, orderID)
	e.clk.Advance(time.Duration(e.deps.Cfg.Trial.AcceptanceWindowHours+1) * time.Hour)
	_, _, _, err := e.services.Trial.Escalate(e.ctx(), trial.ID)
	require.NoError(t, err)

	converted, order, err := e.services.Trial.Convert(e.ctx(), trial.ID, md.managerID)
	require.NoError(t, err)
	assert.Equal(t, domain.TrialConverted, converted.Status)
	assert.Equal(t, domain.HandlingRentSale, order.HandlingMode)

	again, _, err := e.services.Trial.Convert(e.ctx(), trial.ID, md.managerID)
	require.NoError(t, err)
	assert.Equal(t, converted.ID, again.ID)
	assert.Equal(t, domain.TrialConverted, again.Status)
}

func TestTrialListOverdueReturnsEscalated(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := provisionedOrder(t, e, md)
	trial := trialOf(t, e, orderID)
	e.clk.Advance(time.Duration(e.deps.Cfg.Trial.AcceptanceWindowHours+1) * time.Hour)
	_, _, _, _ = e.services.Trial.Escalate(e.ctx(), trial.ID)

	overdue, err := e.services.Trial.ListOverdue(e.ctx())
	require.NoError(t, err)
	require.Len(t, overdue, 1)
	assert.Equal(t, domain.TrialEscalated, overdue[0].Status)
}
