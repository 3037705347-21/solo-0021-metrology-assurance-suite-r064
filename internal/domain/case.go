package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type CaseState string

const (
	CaseAwaitingMeasurement CaseState = "awaiting_measurement"
	CaseReviewReady         CaseState = "review_ready"
	CaseRecheckRequired     CaseState = "recheck_required"
	CaseSealed              CaseState = "sealed"
	CaseCanceled            CaseState = "canceled"
)

type VerificationCase struct {
	ID           string     `json:"id"`
	DeviceID     string     `json:"device_id"`
	Nominal      float64    `json:"nominal"`
	Tolerance    float64    `json:"tolerance"`
	Steward      string     `json:"steward"`
	State        CaseState  `json:"state"`
	Version      int        `json:"version"`
	OpenedAt     time.Time  `json:"opened_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	SealedAt     *time.Time `json:"sealed_at,omitempty"`
	CanceledAt   *time.Time `json:"canceled_at,omitempty"`
	CanceledBy   string     `json:"canceled_by,omitempty"`
	CancelReason string     `json:"cancel_reason,omitempty"`
}

func NewVerificationCase(id, deviceID string, nominal, tolerance float64, steward string, at time.Time) (VerificationCase, error) {
	item := VerificationCase{
		ID:        strings.TrimSpace(id),
		DeviceID:  strings.TrimSpace(deviceID),
		Nominal:   nominal,
		Tolerance: tolerance,
		Steward:   strings.TrimSpace(steward),
		State:     CaseAwaitingMeasurement,
		Version:   1,
		OpenedAt:  at.UTC(),
		UpdatedAt: at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return VerificationCase{}, err
	}
	return item, nil
}

func (c VerificationCase) Validate() error {
	if c.ID == "" || c.DeviceID == "" || c.Steward == "" {
		return fmt.Errorf("%w: case id, device id, and steward are required", ErrInvalidInput)
	}
	if !isFinite(c.Nominal) || !isFinite(c.Tolerance) || c.Tolerance <= 0 {
		return fmt.Errorf("%w: nominal must be finite and tolerance must be positive", ErrInvalidInput)
	}
	if c.Version < 1 || c.OpenedAt.IsZero() || c.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: case version and timestamps are required", ErrInvalidInput)
	}
	switch c.State {
	case CaseAwaitingMeasurement, CaseReviewReady, CaseRecheckRequired, CaseSealed, CaseCanceled:
	default:
		return fmt.Errorf("%w: unsupported case state %q", ErrInvalidInput, c.State)
	}
	if c.State == CaseCanceled {
		if c.CanceledAt == nil || c.CanceledBy == "" || c.CancelReason == "" {
			return fmt.Errorf("%w: canceled case requires an actor, reason, and cancellation time", ErrInvalidInput)
		}
	} else if c.CanceledAt != nil || c.CanceledBy != "" || c.CancelReason != "" {
		return fmt.Errorf("%w: cancellation records are only valid on a canceled case", ErrInvalidInput)
	}
	if c.State == CaseSealed && c.SealedAt == nil {
		return fmt.Errorf("%w: sealed case requires a seal time", ErrInvalidInput)
	}
	return nil
}

func (c VerificationCase) IsUnfinished() bool {
	return c.State != CaseSealed && c.State != CaseCanceled
}

func (c *VerificationCase) RecordMeasurement(accepted bool, at time.Time) error {
	if c.State != CaseAwaitingMeasurement {
		return fmt.Errorf("%w: case is not awaiting measurement", ErrStateConflict)
	}
	if accepted {
		c.State = CaseReviewReady
	} else {
		c.State = CaseRecheckRequired
	}
	c.Version++
	c.UpdatedAt = at.UTC()
	return nil
}

func (c *VerificationCase) Reopen(at time.Time) error {
	if c.State != CaseRecheckRequired {
		return fmt.Errorf("%w: only a recheck_required case can be reopened", ErrStateConflict)
	}
	c.State = CaseAwaitingMeasurement
	c.Version++
	c.UpdatedAt = at.UTC()
	return nil
}

// Cancel abandons a case that was opened against the wrong device, nominal
// value, or tolerance before any measurement was recorded. The actor and
// reason are retained so historical reads can explain the termination. A case
// that already carries measurements, or that has progressed to review,
// recheck, or seal, cannot be canceled.
func (c *VerificationCase) Cancel(actor, reason string, measurementCount int, at time.Time) error {
	if c.State != CaseAwaitingMeasurement {
		return fmt.Errorf("%w: only an awaiting_measurement case can be canceled", ErrStateConflict)
	}
	if measurementCount > 0 {
		return fmt.Errorf("%w: a case with recorded measurements cannot be canceled", ErrStateConflict)
	}
	actor = strings.TrimSpace(actor)
	reason = strings.TrimSpace(reason)
	if actor == "" || reason == "" {
		return fmt.Errorf("%w: actor and reason are required to cancel a case", ErrInvalidInput)
	}
	stamp := at.UTC()
	c.State = CaseCanceled
	c.CanceledAt = &stamp
	c.CanceledBy = actor
	c.CancelReason = reason
	c.Version++
	c.UpdatedAt = stamp
	return nil
}

func (c *VerificationCase) Seal(at time.Time) error {
	if c.State != CaseReviewReady {
		return fmt.Errorf("%w: case is not ready for review", ErrStateConflict)
	}
	stamp := at.UTC()
	c.State = CaseSealed
	c.Version++
	c.UpdatedAt = stamp
	c.SealedAt = &stamp
	return nil
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
