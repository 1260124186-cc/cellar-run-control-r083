package domain

import (
	"math"
	"time"
)

// FrozenFormula captures the approved formula at the moment a run completes.
// Later retirement or any other aggregate change never rewrites these values.
type FrozenFormula struct {
	ID                    string        `json:"id"`
	Name                  string        `json:"name"`
	Style                 string        `json:"style"`
	Version               int64         `json:"version"`
	TargetOriginalGravity float64       `json:"target_original_gravity"`
	TargetFinalGravity    float64       `json:"target_final_gravity"`
	MaxFermentationTempC  float64       `json:"max_fermentation_temp_c"`
	MinimumDays           int           `json:"minimum_days"`
	MaximumDays           int           `json:"maximum_days"`
	Steps                 []FormulaStep `json:"steps"`
	ApprovedAt            *time.Time    `json:"approved_at,omitempty"`
}

// FrozenVessel captures the vessel association at completion, including the
// cleaning state the vessel enters when the run finishes.
type FrozenVessel struct {
	ID        string      `json:"id"`
	Code      string      `json:"code"`
	CapacityL float64     `json:"capacity_liters"`
	State     VesselState `json:"state"`
	Version   int64       `json:"version"`
}

// ReportDeviations compares the observed run with the frozen formula plan.
//
// FinalGravityDelta is the last observed gravity minus the target final
// gravity; the target is met when the measured final gravity is at or below
// the target. MaxTemperatureExcessC is the highest observed temperature minus
// the allowed maximum (negative values are headroom). The day deltas compare
// the elapsed fermentation time against the planned minimum and maximum.
type ReportDeviations struct {
	FinalGravityDelta      float64 `json:"final_gravity_delta"`
	FinalGravityTargetMet  bool    `json:"final_gravity_target_met"`
	MaxObservedTempC       float64 `json:"max_observed_temperature_c"`
	MaxTemperatureExcessC  float64 `json:"max_temperature_excess_c"`
	TemperatureWithinLimit bool    `json:"temperature_within_limit"`
	ElapsedDays            float64 `json:"elapsed_days"`
	MinimumDaysDelta       float64 `json:"minimum_days_delta"`
	MaximumDaysDelta       float64 `json:"maximum_days_delta"`
	DurationWithinPlan     bool    `json:"duration_within_plan"`
}

// CompletionReport is the long-lived, immutable record produced exactly once
// when a run completes. It owns deep copies of every value it exposes, so
// later formula, vessel, or run changes cannot alter the frozen snapshot.
type CompletionReport struct {
	RunID        string               `json:"run_id"`
	RunCode      string               `json:"run_code"`
	State        RunState             `json:"state"`
	Formula      FrozenFormula        `json:"formula"`
	Vessel       FrozenVessel         `json:"vessel"`
	Observations []GravityObservation `json:"observations"`
	Metrics      RunMetrics           `json:"metrics"`
	Deviations   ReportDeviations     `json:"deviations"`
	GeneratedAt  time.Time            `json:"generated_at"`
}

func (r CompletionReport) Clone() CompletionReport {
	cloned := r
	cloned.Formula.Steps = append([]FormulaStep(nil), r.Formula.Steps...)
	cloned.Observations = append([]GravityObservation(nil), r.Observations...)
	if r.Formula.ApprovedAt != nil {
		value := *r.Formula.ApprovedAt
		cloned.Formula.ApprovedAt = &value
	}
	return cloned
}

// NewCompletionReport freezes a completed run together with the approved
// formula and the vessel captured at completion. It refuses incomplete input
// so a report is never persisted with missing data; the caller aborts the
// surrounding transaction when it returns an error.
func NewCompletionReport(
	run FermentationRun,
	formula Formula,
	vessel Vessel,
) (CompletionReport, error) {
	if run.State != RunCompleted {
		return CompletionReport{}, NewError(CodeRunState,
			"completion report requires a completed run")
	}
	if run.Metrics == nil {
		return CompletionReport{}, NewError(CodeRunState,
			"completion report requires calculated final metrics")
	}
	if run.StartedAt == nil || run.EndedAt == nil {
		return CompletionReport{}, NewError(CodeRunState,
			"completion report requires start and completion timestamps")
	}
	if run.VesselID == nil {
		return CompletionReport{}, NewError(CodeRunState,
			"completion report requires a reserved vessel")
	}
	if len(run.Observations) < 3 {
		return CompletionReport{}, NewError(CodeRunState,
			"completion report requires the completed observation sequence")
	}
	if formula.ID == "" || formula.ID != run.FormulaID {
		return CompletionReport{}, NewError(CodeFormulaNotFound,
			"attached formula was not found for the completion report")
	}
	if formula.State != FormulaApproved {
		return CompletionReport{}, NewError(CodeFormulaState,
			"completion report requires an approved formula")
	}
	if vessel.ID == "" || vessel.ID != *run.VesselID {
		return CompletionReport{}, NewError(CodeVesselNotFound,
			"attached vessel was not found for the completion report")
	}
	final := run.Observations[len(run.Observations)-1]

	maxTemp := run.Observations[0].TemperatureC
	for _, observation := range run.Observations[1:] {
		if observation.TemperatureC > maxTemp {
			maxTemp = observation.TemperatureC
		}
	}

	elapsedDays := run.EndedAt.Sub(*run.StartedAt).Seconds() / 86400
	minimumDelta := elapsedDays - float64(formula.MinimumDays)
	maximumDelta := elapsedDays - float64(formula.MaximumDays)
	gravityDelta := final.Gravity - formula.TargetFinalGravity
	tempExcess := maxTemp - formula.MaxFermentationTempC

	var approvedAt *time.Time
	if formula.ApprovedAt != nil {
		value := *formula.ApprovedAt
		approvedAt = &value
	}

	report := CompletionReport{
		RunID:   run.ID,
		RunCode: run.Code,
		State:   RunCompleted,
		Formula: FrozenFormula{
			ID:                    formula.ID,
			Name:                  formula.Name,
			Style:                 formula.Style,
			Version:               formula.Version,
			TargetOriginalGravity: formula.TargetOriginalGravity,
			TargetFinalGravity:    formula.TargetFinalGravity,
			MaxFermentationTempC:  formula.MaxFermentationTempC,
			MinimumDays:           formula.MinimumDays,
			MaximumDays:           formula.MaximumDays,
			Steps:                 append([]FormulaStep(nil), formula.Steps...),
			ApprovedAt:            approvedAt,
		},
		Vessel: FrozenVessel{
			ID:        vessel.ID,
			Code:      vessel.Code,
			CapacityL: vessel.CapacityL,
			State:     vessel.State,
			Version:   vessel.Version,
		},
		Observations: append([]GravityObservation(nil), run.Observations...),
		Metrics:      *run.Metrics,
		Deviations: ReportDeviations{
			FinalGravityDelta:      round3(gravityDelta),
			FinalGravityTargetMet:  gravityDelta <= 0,
			MaxObservedTempC:       round2(maxTemp),
			MaxTemperatureExcessC:  round2(tempExcess),
			TemperatureWithinLimit: maxTemp <= formula.MaxFermentationTempC,
			ElapsedDays:            round2(elapsedDays),
			MinimumDaysDelta:       round2(minimumDelta),
			MaximumDaysDelta:       round2(maximumDelta),
			DurationWithinPlan: elapsedDays >= float64(formula.MinimumDays) &&
				elapsedDays <= float64(formula.MaximumDays),
		},
		GeneratedAt: run.EndedAt.UTC(),
	}
	return report, nil
}

func round3(value float64) float64 {
	return math.Round(value*1000) / 1000
}
