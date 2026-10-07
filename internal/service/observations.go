package service

import "github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"

func (s *Service) AppendObservation(
	id string,
	input ObservationInput,
) (domain.FermentationRun, error) {
	var updated domain.FermentationRun
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		run, ok := snapshot.Runs[id]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "run was not found")
		}
		now := s.clock.Now()
		observation := domain.GravityObservation{
			RunID:        run.ID,
			Sequence:     input.Sequence,
			Gravity:      input.Gravity,
			TemperatureC: input.TemperatureC,
			ObservedAt:   input.ObservedAt.UTC(),
			Note:         input.Note,
			ReceivedAt:   now,
		}
		if err := run.AddObservation(observation); err != nil {
			return err
		}
		snapshot.Runs[id] = run.Clone()
		updated = run.Clone()
		return nil
	})
	if err != nil {
		return domain.FermentationRun{}, err
	}
	return updated, nil
}

func (s *Service) BeginConditioning(
	id string,
	input VersionInput,
) (domain.FermentationRun, error) {
	var updated domain.FermentationRun
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		run, ok := snapshot.Runs[id]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "run was not found")
		}
		if err := s.checkExpected(run.Version, input.ExpectedVersion); err != nil {
			return err
		}
		if err := run.BeginConditioning(s.clock.Now()); err != nil {
			return err
		}
		snapshot.Runs[id] = run.Clone()
		updated = run.Clone()
		return nil
	})
	if err != nil {
		return domain.FermentationRun{}, err
	}
	return updated, nil
}

func (s *Service) CompleteRun(
	id string,
	input VersionInput,
) (domain.FermentationRun, domain.Vessel, domain.CompletionReport, error) {
	var updatedRun domain.FermentationRun
	var updatedVessel domain.Vessel
	var report domain.CompletionReport
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		run, ok := snapshot.Runs[id]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "run was not found")
		}
		if err := s.checkExpected(run.Version, input.ExpectedVersion); err != nil {
			return err
		}
		formula, ok := snapshot.Formulas[run.FormulaID]
		if !ok {
			return domain.NewError(domain.CodeFormulaNotFound, "attached formula was not found")
		}
		if err := run.Complete(formula.TargetOriginalGravity, s.clock.Now()); err != nil {
			return err
		}
		if run.VesselID == nil {
			return domain.NewError(domain.CodeRunState, "run has no vessel")
		}
		vessel, ok := snapshot.Vessels[*run.VesselID]
		if !ok {
			return domain.NewError(domain.CodeVesselNotFound, "vessel was not found")
		}
		if err := vessel.StartCleaning(run.ID, s.clock.Now()); err != nil {
			return err
		}
		// Reports are written once, in the same transaction that completes the
		// run, so an existing report can never be regenerated or rewritten.
		if _, exists := snapshot.Reports[id]; exists {
			return domain.NewError(domain.CodeReportConflict,
				"a completion report already exists for this run")
		}
		frozen, err := domain.NewCompletionReport(run, formula, vessel)
		if err != nil {
			return err
		}
		snapshot.Runs[id] = run.Clone()
		snapshot.Vessels[vessel.ID] = vessel
		snapshot.Reports[id] = frozen
		updatedRun = run.Clone()
		updatedVessel = vessel
		report = frozen.Clone()
		return nil
	})
	if err != nil {
		return domain.FermentationRun{}, domain.Vessel{}, domain.CompletionReport{}, err
	}
	return updatedRun, updatedVessel, report, nil
}

// GetRunReport reads the frozen completion report. Unknown runs, unfinished
// runs, and completed runs whose snapshot predates report generation each
// return an explicit stable error instead of an empty document.
func (s *Service) GetRunReport(id string) (domain.CompletionReport, error) {
	var found domain.CompletionReport
	err := s.store.View(func(snapshot domain.Snapshot) error {
		run, ok := snapshot.Runs[id]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "run was not found")
		}
		report, ok := snapshot.Reports[id]
		if !ok {
			if run.State == domain.RunCompleted {
				return domain.NewError(domain.CodeReportNotReady,
					"completed run has no frozen completion report")
			}
			return domain.NewError(domain.CodeReportNotReady,
				"run has not completed, so no completion report exists")
		}
		found = report.Clone()
		return nil
	})
	if err != nil {
		return domain.CompletionReport{}, err
	}
	return found, nil
}
