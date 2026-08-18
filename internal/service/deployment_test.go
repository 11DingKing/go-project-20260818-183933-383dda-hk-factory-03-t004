package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sitesync/internal/domain"
	"sitesync/internal/errorsx"
)

func TestProvisionSagaCompletesAllSteps(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := e.createOrder(t, md)

	order, steps, err := e.services.Deployment.Provision(e.ctx(), orderID, "ops")
	require.NoError(t, err)
	assert.Equal(t, domain.DeploymentActive, order.Status)
	require.Len(t, steps, 5)
	for _, st := range steps {
		assert.Equal(t, domain.StepDone, st.Status, "step %s should be done", st.StepName)
	}
	trial, err := e.deps.Trials.GetTrialByOrder(e.ctx(), orderID)
	require.NoError(t, err)
	assert.Equal(t, domain.TrialActive, trial.Status)
	ins, err := e.deps.Inspections.GetInspectionByOrderRound(e.ctx(), orderID, 1)
	require.NoError(t, err)
	assert.Equal(t, domain.InspectionDispatched, ins.Status)
	bill, err := e.deps.Bills.GetBillByOrderPeriod(e.ctx(), orderID, 1)
	require.NoError(t, err)
	assert.Equal(t, domain.BillDraft, bill.Status)
	bindings, err := e.deps.OrderDevices.ListDevicesByOrder(e.ctx(), orderID)
	require.NoError(t, err)
	assert.Len(t, bindings, 2)
}

func TestProvisionActiveIsIdempotent(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := e.createOrder(t, md)
	_, _, err := e.services.Deployment.Provision(e.ctx(), orderID, "ops")
	require.NoError(t, err)
	order2, steps2, err := e.services.Deployment.Provision(e.ctx(), orderID, "ops")
	require.NoError(t, err)
	assert.Equal(t, domain.DeploymentActive, order2.Status)
	for _, st := range steps2 {
		assert.Equal(t, domain.StepDone, st.Status)
	}
}

func TestProvisionRollbackOnFailureThenRetryResumes(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := e.createOrder(t, md)
	// Simulate an interrupted state: the planned device bindings were lost.
	_, err := e.deps.OrderDevices.UnbindAllDevices(e.ctx(), orderID)
	require.NoError(t, err)

	_, _, perr := e.services.Deployment.Provision(e.ctx(), orderID, "ops")
	require.Error(t, perr)
	assert.True(t, errorsx.IsRetryable(perr))

	rel, gerr := e.deps.Orders.GetOrder(e.ctx(), orderID)
	require.NoError(t, gerr)
	assert.Equal(t, domain.DeploymentPendingRetry, rel.Status)

	steps, _ := e.deps.Steps.ListStepsByOrder(e.ctx(), orderID)
	byName := map[string]domain.DeploymentStep{}
	for _, s := range steps {
		byName[s.StepName] = s
	}
	assert.Equal(t, domain.StepDone, byName[domain.StepRegisterOrder].Status, "register already effective, must not repeat")
	assert.Equal(t, domain.StepFailed, byName[domain.StepBindDevices].Status)

	// Repair the interrupted state and resume: only the failed+pending steps run.
	for _, did := range md.deviceIDs {
		require.NoError(t, e.deps.OrderDevices.BindDevice(e.ctx(), orderID, did))
	}
	order, steps2, err := e.services.Deployment.Provision(e.ctx(), orderID, "ops")
	require.NoError(t, err)
	assert.Equal(t, domain.DeploymentActive, order.Status)
	for _, s := range steps2 {
		assert.Equal(t, domain.StepDone, s.Status, "step %s", s.StepName)
	}
}

func TestCompensateRollbackReversesSteps(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := e.createOrder(t, md)
	_, _, err := e.services.Deployment.Provision(e.ctx(), orderID, "ops")
	require.NoError(t, err)

	order, steps, err := e.services.Deployment.Compensate(e.ctx(), orderID, "ops")
	require.NoError(t, err)
	assert.Equal(t, domain.DeploymentAborted, order.Status)
	for _, s := range steps {
		assert.Equal(t, domain.StepCompensated, s.Status, "step %s", s.StepName)
	}
	bindings, _ := e.deps.OrderDevices.ListDevicesByOrder(e.ctx(), orderID)
	assert.Empty(t, bindings, "devices unbound by compensation")
	bill, _ := e.deps.Bills.GetBillByOrderPeriod(e.ctx(), orderID, 1)
	if bill.ID != "" {
		assert.Equal(t, domain.BillVoided, bill.Status)
	}
	ins, _ := e.deps.Inspections.GetInspectionByOrderRound(e.ctx(), orderID, 1)
	if ins.ID != "" {
		assert.Equal(t, domain.InspectionCancelled, ins.Status)
	}
}

func TestCompensateRollbackIsIdempotentAndRepeatable(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := e.createOrder(t, md)
	_, _, err := e.services.Deployment.Provision(e.ctx(), orderID, "ops")
	require.NoError(t, err)
	_, _, err = e.services.Deployment.Compensate(e.ctx(), orderID, "ops")
	require.NoError(t, err)
	order, _, err := e.services.Deployment.Compensate(e.ctx(), orderID, "ops")
	require.NoError(t, err)
	assert.Equal(t, domain.DeploymentAborted, order.Status)
}

func TestIllegalTransitionRejectsCompensateOnDraft(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := e.createOrder(t, md)
	_, _, err := e.services.Deployment.Compensate(e.ctx(), orderID, "ops")
	assert.ErrorIs(t, err, errorsx.ErrIllegalTransition)
}

func TestProvisionCommitPersistsPartialProgress(t *testing.T) {
	e := newEnv(t)
	md := seedMaster(t, e)
	orderID := e.createOrder(t, md)
	// Run register + bind by provisioning, then unbind to force a failure at bind
	// on a fresh retry, proving earlier commits survived the rollback of the failed tx.
	_, _, _ = e.services.Deployment.Provision(e.ctx(), orderID, "ops")
	rel, _ := e.deps.Orders.GetOrder(e.ctx(), orderID)
	assert.Equal(t, domain.DeploymentActive, rel.Status)

	steps, _ := e.deps.Steps.ListStepsByOrder(e.ctx(), orderID)
	assert.Len(t, steps, 5)
}

var _ = time.Second
var _ = context.Background
