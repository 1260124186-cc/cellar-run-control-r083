package service

import (
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// GenerateRunReport freezes a completion report for a completed run that does
// not have one yet. An existing report is never rewritten.
func (s *Service) GenerateRunReport(runID string) (domain.CompletionReport, error) {
	var generated domain.CompletionReport
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		if _, exists := snapshot.Reports[runID]; exists {
			return domain.NewError(domain.CodeReportExists,
				"a completion report already exists for this run")
		}
		report, err := buildRunReport(snapshot, runID, s.clock.Now())
		if err != nil {
			return err
		}
		snapshot.Reports[runID] = report
		generated = report.Clone()
		return nil
	})
	if err != nil {
		return domain.CompletionReport{}, err
	}
	return generated, nil
}

func (s *Service) GetRunReport(runID string) (domain.CompletionReport, error) {
	var found domain.CompletionReport
	err := s.store.View(func(snapshot domain.Snapshot) error {
		if _, ok := snapshot.Runs[runID]; !ok {
			return domain.NewError(domain.CodeRunNotFound, "run was not found")
		}
		report, ok := snapshot.Reports[runID]
		if !ok {
			return domain.NewError(domain.CodeReportNotFound,
				"the run does not have a completion report")
		}
		found = report.Clone()
		return nil
	})
	if err != nil {
		return domain.CompletionReport{}, err
	}
	return found, nil
}

// buildRunReport assembles a report from the current snapshot state. The
// caller is responsible for storing the result.
func buildRunReport(
	snapshot *domain.Snapshot,
	runID string,
	now time.Time,
) (domain.CompletionReport, error) {
	run, ok := snapshot.Runs[runID]
	if !ok {
		return domain.CompletionReport{}, domain.NewError(domain.CodeRunNotFound,
			"run was not found")
	}
	if run.State != domain.RunCompleted {
		return domain.CompletionReport{}, domain.NewError(domain.CodeRunState,
			"only a completed run can produce a completion report")
	}
	formula, ok := snapshot.Formulas[run.FormulaID]
	if !ok {
		return domain.CompletionReport{}, domain.NewError(domain.CodeFormulaNotFound,
			"attached formula was not found")
	}
	if run.VesselID == nil {
		return domain.CompletionReport{}, domain.NewError(domain.CodeReportDataMissing,
			"completed run has no vessel to freeze into the report")
	}
	vessel, ok := snapshot.Vessels[*run.VesselID]
	if !ok {
		return domain.CompletionReport{}, domain.NewError(domain.CodeVesselNotFound,
			"attached vessel was not found")
	}
	id, err := generateEntityID("report")
	if err != nil {
		return domain.CompletionReport{}, domain.NewError(domain.CodeStorage, err.Error())
	}
	return domain.NewCompletionReport(id, run, formula, vessel, now)
}
