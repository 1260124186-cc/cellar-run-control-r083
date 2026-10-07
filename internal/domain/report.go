package domain

import "time"

// ReportFormula freezes the formula identity, version, and targets that were
// in effect when the run completed. Later formula changes never rewrite it.
type ReportFormula struct {
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`
	Style                 string  `json:"style"`
	Version               int64   `json:"version"`
	TargetOriginalGravity float64 `json:"target_original_gravity"`
	TargetFinalGravity    float64 `json:"target_final_gravity"`
	MaxFermentationTempC  float64 `json:"max_fermentation_temp_c"`
}

// ReportVessel freezes the vessel identity and version at completion time.
type ReportVessel struct {
	ID        string  `json:"id"`
	Code      string  `json:"code"`
	CapacityL float64 `json:"capacity_liters"`
	Version   int64   `json:"version"`
}

// ReportDeviations compares the frozen run outcome against the frozen formula
// targets. Positive values mean the run finished above the target.
type ReportDeviations struct {
	FinalGravityDeviation   float64 `json:"final_gravity_deviation"`
	MaxObservedTemperatureC float64 `json:"max_observed_temperature_c"`
	TemperatureDeviationC   float64 `json:"temperature_deviation_c"`
}

// CompletionReport is an immutable snapshot of a completed run. It is frozen
// when the run completes and is never rewritten by later formula, vessel, or
// run changes.
type CompletionReport struct {
	ID           string               `json:"id"`
	RunID        string               `json:"run_id"`
	RunCode      string               `json:"run_code"`
	Formula      ReportFormula        `json:"formula"`
	Vessel       ReportVessel         `json:"vessel"`
	Observations []GravityObservation `json:"observations"`
	Metrics      RunMetrics           `json:"metrics"`
	Deviations   ReportDeviations     `json:"deviations"`
	GeneratedAt  time.Time            `json:"generated_at"`
	Version      int64                `json:"version"`
}

func NewCompletionReport(
	id string,
	run FermentationRun,
	formula Formula,
	vessel Vessel,
	now time.Time,
) (CompletionReport, error) {
	if run.State != RunCompleted {
		return CompletionReport{}, NewError(CodeRunState,
			"only a completed run can produce a completion report")
	}
	if run.Metrics == nil || len(run.Observations) == 0 {
		return CompletionReport{}, NewError(CodeReportDataMissing,
			"completed run is missing the metrics or observations required for a report")
	}
	observations := append([]GravityObservation(nil), run.Observations...)
	maxTemperature := observations[0].TemperatureC
	for _, observation := range observations[1:] {
		if observation.TemperatureC > maxTemperature {
			maxTemperature = observation.TemperatureC
		}
	}
	finalGravity := observations[len(observations)-1].Gravity
	return CompletionReport{
		ID:      id,
		RunID:   run.ID,
		RunCode: run.Code,
		Formula: ReportFormula{
			ID:                    formula.ID,
			Name:                  formula.Name,
			Style:                 formula.Style,
			Version:               formula.Version,
			TargetOriginalGravity: formula.TargetOriginalGravity,
			TargetFinalGravity:    formula.TargetFinalGravity,
			MaxFermentationTempC:  formula.MaxFermentationTempC,
		},
		Vessel: ReportVessel{
			ID:        vessel.ID,
			Code:      vessel.Code,
			CapacityL: vessel.CapacityL,
			Version:   vessel.Version,
		},
		Observations: observations,
		Metrics:      *run.Metrics,
		Deviations: ReportDeviations{
			FinalGravityDeviation:   round3(finalGravity - formula.TargetFinalGravity),
			MaxObservedTemperatureC: maxTemperature,
			TemperatureDeviationC:   round2(maxTemperature - formula.MaxFermentationTempC),
		},
		GeneratedAt: now.UTC(),
		Version:     1,
	}, nil
}

func (r CompletionReport) Clone() CompletionReport {
	cloned := r
	cloned.Observations = append([]GravityObservation(nil), r.Observations...)
	return cloned
}
