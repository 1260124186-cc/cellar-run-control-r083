package workflowcheck

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/httpapi"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

type reportFormulaView struct {
	ID                    string               `json:"id"`
	Name                  string               `json:"name"`
	Style                 string               `json:"style"`
	Version               int64                `json:"version"`
	TargetOriginalGravity float64              `json:"target_original_gravity"`
	TargetFinalGravity    float64              `json:"target_final_gravity"`
	MaxFermentationTempC  float64              `json:"max_fermentation_temp_c"`
	MinimumDays           int                  `json:"minimum_days"`
	MaximumDays           int                  `json:"maximum_days"`
	Steps                 []domain.FormulaStep `json:"steps"`
}

type reportVesselView struct {
	ID        string             `json:"id"`
	Code      string             `json:"code"`
	CapacityL float64            `json:"capacity_liters"`
	State     domain.VesselState `json:"state"`
	Version   int64              `json:"version"`
}

type reportView struct {
	RunID        string                      `json:"run_id"`
	RunCode      string                      `json:"run_code"`
	State        domain.RunState             `json:"state"`
	Formula      reportFormulaView           `json:"formula"`
	Vessel       reportVesselView            `json:"vessel"`
	Observations []domain.GravityObservation `json:"observations"`
	Metrics      domain.RunMetrics           `json:"metrics"`
	Deviations   struct {
		FinalGravityDelta      float64 `json:"final_gravity_delta"`
		FinalGravityTargetMet  bool    `json:"final_gravity_target_met"`
		MaxObservedTempC       float64 `json:"max_observed_temperature_c"`
		MaxTemperatureExcessC  float64 `json:"max_temperature_excess_c"`
		TemperatureWithinLimit bool    `json:"temperature_within_limit"`
		ElapsedDays            float64 `json:"elapsed_days"`
		MinimumDaysDelta       float64 `json:"minimum_days_delta"`
		MaximumDaysDelta       float64 `json:"maximum_days_delta"`
		DurationWithinPlan     bool    `json:"duration_within_plan"`
	} `json:"deviations"`
	GeneratedAt time.Time `json:"generated_at"`
}

type completionView struct {
	Run    runView    `json:"run"`
	Vessel vesselView `json:"vessel"`
	Report reportView `json:"report"`
}

func completionReport(api *client, dataDir string, stopServer func()) error {
	formula, err := ensureApprovedFormula(api, "Report Formula")
	if err != nil {
		return err
	}
	vessel, err := createCheckVessel(api, "VSL-303", 500)
	if err != nil {
		return err
	}
	started, err := createStartedRun(api, formula.ID, vessel.ID, "RUN-REPORT")
	if err != nil {
		return err
	}
	runID := started.Run.ID

	openResponse, err := api.call("POST", "/v1/runs",
		createRunPayload("RUN-REPORT-OPEN", formula.ID), nil)
	if err != nil {
		return err
	}
	if err := requireStatus(openResponse, 201, "create open run"); err != nil {
		return err
	}
	var openRun runView
	if err := openResponse.decode(&openRun); err != nil {
		return err
	}

	notReady, err := api.call("GET", "/v1/runs/"+runID+"/report", nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(notReady, 409, string(domain.CodeReportNotReady),
		"read report before completion"); err != nil {
		return err
	}

	openReport, err := api.call("GET", "/v1/runs/"+openRun.ID+"/report", nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(openReport, 409, string(domain.CodeReportNotReady),
		"read report for unfinished run"); err != nil {
		return err
	}

	missingRun, err := api.call("GET", "/v1/runs/run_unknown00/report", nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(missingRun, 404, string(domain.CodeRunNotFound),
		"read report for unknown run"); err != nil {
		return err
	}

	base := time.Now().UTC().Add(10 * time.Second)
	points := []struct {
		gravity     float64
		temperature float64
	}{
		{gravity: 1.050, temperature: 20.0},
		{gravity: 1.028, temperature: 20.5},
		{gravity: 1.014, temperature: 19.8},
	}
	version := started.Run.Version
	for index, point := range points {
		sequence := index + 1
		response, callErr := api.call("POST", "/v1/runs/"+runID+"/observations",
			map[string]any{
				"sequence":      sequence,
				"gravity":       point.gravity,
				"temperature_c": point.temperature,
				"observed_at":   verifiedAt(base, sequence),
				"note":          fmt.Sprintf("observation %d", sequence),
			}, ptrInt64(version))
		if callErr != nil {
			return callErr
		}
		if err := requireStatus(response, 200, "append report observation"); err != nil {
			return err
		}
		var updated runView
		if err := response.decode(&updated); err != nil {
			return err
		}
		version = updated.Version
	}

	conditioningResponse, err := api.call("POST", "/v1/runs/"+runID+"/conditioning",
		nil, ptrInt64(version))
	if err != nil {
		return err
	}
	if err := requireStatus(conditioningResponse, 200, "begin conditioning for report"); err != nil {
		return err
	}
	var conditioning runView
	if err := conditioningResponse.decode(&conditioning); err != nil {
		return err
	}

	completeResponse, err := api.call("POST", "/v1/runs/"+runID+"/complete",
		nil, ptrInt64(conditioning.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(completeResponse, 200, "complete run with report"); err != nil {
		return err
	}
	var completed completionView
	if err := completeResponse.decode(&completed); err != nil {
		return err
	}
	if err := validateFrozenReport(completed.Report, completed.Run,
		formula.ID, vessel.ID); err != nil {
		return err
	}

	repeatResponse, err := api.call("POST", "/v1/runs/"+runID+"/complete",
		nil, ptrInt64(completed.Run.Version))
	if err != nil {
		return err
	}
	if err := requireCode(repeatResponse, 409, string(domain.CodeRunState),
		"repeat completion after report generation"); err != nil {
		return err
	}

	readResponse, err := api.call("GET", "/v1/runs/"+runID+"/report", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(readResponse, 200, "read frozen report"); err != nil {
		return err
	}
	var stored reportView
	if err := readResponse.decode(&stored); err != nil {
		return err
	}
	if !reflect.DeepEqual(stored, completed.Report) {
		return errInvalidWorkflow("report endpoint did not return the frozen completion report")
	}

	// Change the live aggregates after completion: finish cleaning and retire
	// the formula. The frozen report must keep its own snapshot.
	finishResponse, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning",
		nil, ptrInt64(completed.Vessel.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(finishResponse, 200, "finish cleaning after report"); err != nil {
		return err
	}
	// The planned open run must be closed before its formula can be retired.
	abortOpenResponse, err := api.call("POST", "/v1/runs/"+openRun.ID+"/abort",
		nil, ptrInt64(openRun.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(abortOpenResponse, 200, "abort open run before retirement"); err != nil {
		return err
	}
	abortedReport, err := api.call("GET", "/v1/runs/"+openRun.ID+"/report", nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(abortedReport, 409, string(domain.CodeReportNotReady),
		"read report for aborted run"); err != nil {
		return err
	}
	retireResponse, err := api.call("POST", "/v1/formulas/"+formula.ID+"/retire",
		nil, ptrInt64(formula.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(retireResponse, 200, "retire formula after report"); err != nil {
		return err
	}

	afterChange, err := api.call("GET", "/v1/runs/"+runID+"/report", nil, nil)
	if err != nil {
		return err
	}
	var afterChangeReport reportView
	if err := afterChange.decode(&afterChangeReport); err != nil {
		return err
	}
	if !reflect.DeepEqual(afterChangeReport, completed.Report) {
		return errInvalidWorkflow("formula or vessel changes rewrote the report snapshot")
	}
	if afterChangeReport.Vessel.State != domain.VesselCleaning {
		return errInvalidWorkflow("frozen vessel state was altered after cleaning finished")
	}
	if afterChangeReport.Formula.Version != formula.Version {
		return errInvalidWorkflow("frozen formula version was altered after retirement")
	}

	// Restart the process against the same data directory: the report must
	// remain readable and byte-for-byte identical, and its final metrics must
	// still agree with the run detail endpoint.
	stopServer()
	restartedServer, err := startCheckAPI(dataDir)
	if err != nil {
		return err
	}
	defer restartedServer.Close()
	restartedAPI := newClient(restartedServer.URL)

	restartReportResponse, err := restartedAPI.call("GET", "/v1/runs/"+runID+"/report", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(restartReportResponse, 200, "read report after restart"); err != nil {
		return err
	}
	if !reflect.DeepEqual(restartReportResponse.body, readResponse.body) {
		return errInvalidWorkflow("report content changed after process restart")
	}
	var restartedReport reportView
	if err := restartReportResponse.decode(&restartedReport); err != nil {
		return err
	}

	restartRunResponse, err := restartedAPI.call("GET", "/v1/runs/"+runID, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(restartRunResponse, 200, "read run detail after restart"); err != nil {
		return err
	}
	var restartedRun runView
	if err := restartRunResponse.decode(&restartedRun); err != nil {
		return err
	}
	if restartedRun.Metrics == nil ||
		!reflect.DeepEqual(*restartedRun.Metrics, restartedReport.Metrics) {
		return errInvalidWorkflow("run detail and report final metrics diverge after restart")
	}

	// A completed run whose report data is missing must produce a clear
	// stable result rather than an invented document.
	restartedServer.Close()
	if err := removePersistedReport(dataDir, runID); err != nil {
		return err
	}
	tamperedServer, err := startCheckAPI(dataDir)
	if err != nil {
		return err
	}
	defer tamperedServer.Close()
	missingReport, err := newClient(tamperedServer.URL).call(
		"GET", "/v1/runs/"+runID+"/report", nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(missingReport, 409, string(domain.CodeReportNotReady),
		"read completed run with missing report data"); err != nil {
		return err
	}
	return nil
}

func validateFrozenReport(
	report reportView,
	run runView,
	formulaID string,
	vesselID string,
) error {
	if report.RunID != run.ID || report.RunCode != run.Code {
		return errInvalidWorkflow("report does not identify its run")
	}
	if report.State != domain.RunCompleted {
		return errInvalidWorkflow("report does not freeze a completed state")
	}
	if report.Formula.ID != formulaID {
		return errInvalidWorkflow("report froze the wrong formula")
	}
	if report.Formula.Version != 3 {
		return errInvalidWorkflow("report did not freeze the approved formula version")
	}
	if len(report.Formula.Steps) != 3 {
		return errInvalidWorkflow("report did not freeze the formula steps")
	}
	if report.Vessel.ID != vesselID || report.Vessel.Code != "VSL-303" {
		return errInvalidWorkflow("report froze the wrong vessel")
	}
	if report.Vessel.State != domain.VesselCleaning || report.Vessel.Version != 4 {
		return errInvalidWorkflow("report did not freeze the vessel cleaning state")
	}
	if len(report.Observations) != 3 {
		return errInvalidWorkflow("report did not freeze the observation sequence")
	}
	for index, observation := range report.Observations {
		if observation.Sequence != index+1 {
			return errInvalidWorkflow("frozen observations lost their sequence")
		}
	}
	if run.Metrics == nil || !reflect.DeepEqual(report.Metrics, *run.Metrics) {
		return errInvalidWorkflow("completion response metrics and report metrics differ")
	}
	dev := report.Deviations
	if dev.FinalGravityDelta != 0.002 || dev.FinalGravityTargetMet {
		return errInvalidWorkflow("final gravity deviation was not frozen correctly")
	}
	if dev.MaxObservedTempC != 20.5 || dev.MaxTemperatureExcessC != -1.5 ||
		!dev.TemperatureWithinLimit {
		return errInvalidWorkflow("temperature deviation was not frozen correctly")
	}
	if dev.ElapsedDays < 0 || dev.MinimumDaysDelta != -7 ||
		dev.MaximumDaysDelta != -21 || dev.DurationWithinPlan {
		return errInvalidWorkflow("planned-duration deviation was not frozen correctly")
	}
	if report.GeneratedAt.IsZero() ||
		run.EndedAt == nil || !report.GeneratedAt.Equal(*run.EndedAt) {
		return errInvalidWorkflow("report generation time was not frozen")
	}
	return nil
}

func startCheckAPI(dataDir string) (*httptest.Server, error) {
	store, err := storage.Open(dataDir)
	if err != nil {
		return nil, err
	}
	app := service.New(store, clock.System{})
	return httptest.NewServer(httpapi.NewHandler(app)), nil
}

func removePersistedReport(dataDir, runID string) error {
	statePath := filepath.Join(dataDir, "state.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return fmt.Errorf("read state for missing-data check: %w", err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("decode state for missing-data check: %w", err)
	}
	reports, ok := data["completion_reports"].(map[string]any)
	if !ok {
		return errInvalidWorkflow("persisted state has no completion report collection")
	}
	delete(reports, runID)
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state for missing-data check: %w", err)
	}
	if err := os.WriteFile(statePath, encoded, 0o644); err != nil {
		return fmt.Errorf("rewrite state for missing-data check: %w", err)
	}
	return nil
}
