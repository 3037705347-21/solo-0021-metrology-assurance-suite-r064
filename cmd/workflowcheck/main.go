package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/bootstrap"
)

type apiResponse struct {
	Status int
	Body   map[string]any
	Raw    string
}

type harness struct {
	baseURL string
	client  *http.Client
	server  *httptest.Server
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to verify")
	flag.Parse()

	checks := map[string]func() error{
		"register-device":          checkRegisterDevice,
		"open-verification-case":   checkOpenVerificationCase,
		"record-measurement":       checkRecordMeasurement,
		"reopen-verification-case": checkReopenVerificationCase,
		"cancel-verification-case": checkCancelVerificationCase,
		"seal-verification":        checkSealVerification,
	}
	run, ok := checks[*workflow]
	if !ok {
		fmt.Fprintf(flag.CommandLine.Output(), "unknown workflow %q\n", *workflow)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Printf("workflow %s: failed: %v\n", *workflow, err)
		os.Exit(1)
	}
	fmt.Printf("workflow %s: ok\n", *workflow)
}

func checkRegisterDevice() error {
	h := newHarness()
	defer h.close()

	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": " met-1001 ",
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return err
	}
	id, err := nestedString(response.Body, "device", "id")
	if err != nil {
		return err
	}
	tag, err := nestedString(response.Body, "device", "asset_tag")
	if err != nil {
		return err
	}
	if tag != "MET-1001" {
		return fmt.Errorf("asset tag was not normalized: %q", tag)
	}

	read, err := h.request(http.MethodGet, "/devices/"+id, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readID, err := nestedString(read.Body, "device", "id")
	if err != nil {
		return err
	}
	if readID != id {
		return fmt.Errorf("read returned device %q, expected %q", readID, id)
	}

	duplicate, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": "MET-1001",
		"model":     "Other Meter",
		"room":      "Room B",
		"steward":   "Mira",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(duplicate, "duplicate_asset_tag")
}

func checkOpenVerificationCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1101")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	state, err := nestedString(opened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("opened case state = %q", state)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}

	second, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(second, "active_case_exists")
}

func checkRecordMeasurement() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1201")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	accepted, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(accepted.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("accepted measurement state = %q", state)
	}
	acceptedValue, err := nestedBool(accepted.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if !acceptedValue {
		return errors.New("in-tolerance measurement was not accepted")
	}

	second, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	rejectDeviceID, err := h.createDevice("MET-1202")
	if err != nil {
		return err
	}
	rejectCase, err := h.openCase(rejectDeviceID)
	if err != nil {
		return err
	}
	rejectCaseID, err := nestedString(rejectCase.Body, "case", "id")
	if err != nil {
		return err
	}
	rejected, err := h.recordMeasurement(rejectCaseID, 26.5, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	rejectedState, err := nestedString(rejected.Body, "case", "state")
	if err != nil {
		return err
	}
	if rejectedState != "recheck_required" {
		return fmt.Errorf("out-of-tolerance state = %q", rejectedState)
	}
	rejectedValue, err := nestedBool(rejected.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if rejectedValue {
		return errors.New("out-of-tolerance measurement was accepted")
	}
	return nil
}

func checkReopenVerificationCase() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.rejectedCase("MET-1301")
	if err != nil {
		return err
	}
	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "measurement setup was replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(reopened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("reopened case state = %q", state)
	}
	measurements, err := nestedSlice(reopened.Body, "measurements")
	if err != nil {
		return err
	}
	if len(measurements) != 1 {
		return fmt.Errorf("reopen changed measurement history: %d entries", len(measurements))
	}

	second, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "duplicate request",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "case_reopened") {
		return errors.New("reopen audit event is missing")
	}
	return nil
}

func checkCancelVerificationCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1501")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}

	canceled, err := h.cancelCase(caseID, "Lin", "case opened against the wrong nominal value")
	if err != nil {
		return err
	}
	if err := expectStatus(canceled, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(canceled.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "canceled" {
		return fmt.Errorf("canceled case state = %q", state)
	}
	canceledBy, err := nestedString(canceled.Body, "case", "canceled_by")
	if err != nil {
		return err
	}
	if canceledBy != "Lin" {
		return fmt.Errorf("canceled_by = %q", canceledBy)
	}
	cancelReason, err := nestedString(canceled.Body, "case", "cancel_reason")
	if err != nil {
		return err
	}
	if cancelReason != "case opened against the wrong nominal value" {
		return fmt.Errorf("cancel_reason = %q", cancelReason)
	}
	canceledAt, err := nestedString(canceled.Body, "case", "canceled_at")
	if err != nil {
		return err
	}
	if canceledAt == "" {
		return errors.New("canceled_at was not recorded")
	}

	// The historical read still explains the cancellation, including the event.
	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readState, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	if readState != "canceled" {
		return fmt.Errorf("historical read state = %q", readState)
	}
	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "case_canceled") {
		return errors.New("cancel audit event is missing")
	}

	// The device's unfinished-case slot is released: a new case can open.
	reopenedDevice, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	if err := expectStatus(reopenedDevice, http.StatusCreated); err != nil {
		return err
	}
	newState, err := nestedString(reopenedDevice.Body, "case", "state")
	if err != nil {
		return err
	}
	if newState != "awaiting_measurement" {
		return fmt.Errorf("reopened device case state = %q", newState)
	}
	newCaseID, err := nestedString(reopenedDevice.Body, "case", "id")
	if err != nil {
		return err
	}
	if newCaseID == caseID {
		return errors.New("new case reused the canceled case id")
	}

	// A canceled case is terminal: no measurement, reopen, seal, or second cancel.
	afterMeasurement, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(afterMeasurement, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(afterMeasurement, "state_conflict"); err != nil {
		return err
	}
	afterReopen, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "attempt to revive",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(afterReopen, http.StatusConflict); err != nil {
		return err
	}
	afterSeal, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer": "Lin",
		"summary":  "attempt to seal",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(afterSeal, http.StatusConflict); err != nil {
		return err
	}
	secondCancel, err := h.cancelCase(caseID, "Lin", "duplicate cancel")
	if err != nil {
		return err
	}
	if err := expectStatus(secondCancel, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(secondCancel, "state_conflict"); err != nil {
		return err
	}

	// Cancel requires an actor and a reason.
	missingActorDevice, err := h.createDevice("MET-1502")
	if err != nil {
		return err
	}
	missingActorCase, err := h.openCase(missingActorDevice)
	if err != nil {
		return err
	}
	missingActorID, err := nestedString(missingActorCase.Body, "case", "id")
	if err != nil {
		return err
	}
	missingActor, err := h.cancelCase(missingActorID, "  ", "reason provided")
	if err != nil {
		return err
	}
	if err := expectStatus(missingActor, http.StatusBadRequest); err != nil {
		return err
	}

	// A case with a measurement is not cancelable, in every progressed state.
	readyID, err := h.readyCase("MET-1503")
	if err != nil {
		return err
	}
	cancelReady, err := h.cancelCase(readyID, "Lin", "too late to cancel")
	if err != nil {
		return err
	}
	if err := expectStatus(cancelReady, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(cancelReady, "state_conflict"); err != nil {
		return err
	}

	recheckID, err := h.rejectedCase("MET-1504")
	if err != nil {
		return err
	}
	cancelRecheck, err := h.cancelCase(recheckID, "Lin", "too late to cancel")
	if err != nil {
		return err
	}
	if err := expectStatus(cancelRecheck, http.StatusConflict); err != nil {
		return err
	}

	sealedID, err := h.sealedCase("MET-1505")
	if err != nil {
		return err
	}
	cancelSealed, err := h.cancelCase(sealedID, "Lin", "cannot cancel sealed")
	if err != nil {
		return err
	}
	if err := expectStatus(cancelSealed, http.StatusConflict); err != nil {
		return err
	}

	// A reopened awaiting_measurement case still carries the rejected
	// measurement, so it cannot take the no-measurement cancellation path.
	reopenedID, err := h.rejectedCase("MET-1506")
	if err != nil {
		return err
	}
	reopened, err := h.request(http.MethodPost, "/cases/"+reopenedID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "setup replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	cancelReopened, err := h.cancelCase(reopenedID, "Lin", "reopened case has a measurement")
	if err != nil {
		return err
	}
	if err := expectStatus(cancelReopened, http.StatusConflict); err != nil {
		return err
	}

	if err := checkCancelMeasurementRace("MET-1507"); err != nil {
		return err
	}
	return nil
}

func checkCancelMeasurementRace(tag string) error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice(tag)
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}

	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		response, err := h.cancelCase(caseID, "Lin", "wrong device, aborting")
		results <- outcome{response: response, err: err}
	}()
	wait.Add(1)
	go func() {
		defer wait.Done()
		response, err := h.recordMeasurement(caseID, 25.1, "Rui")
		results <- outcome{response: response, err: err}
	}()
	wait.Wait()
	close(results)

	cancels := 0
	measurements := 0
	conflicts := 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch {
		case item.response.Status == http.StatusOK:
			cancels++
		case item.response.Status == http.StatusCreated:
			measurements++
		case item.response.Status == http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent cancel/measurement status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if conflicts != 1 || cancels+measurements != 1 {
		return fmt.Errorf("concurrent cancel/measurement outcomes: %d canceled, %d measured, %d conflicts", cancels, measurements, conflicts)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	finalState, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	recorded, err := nestedSlice(read.Body, "measurements")
	if err != nil {
		return err
	}
	switch finalState {
	case "canceled":
		if len(recorded) != 0 {
			return fmt.Errorf("canceled case carries %d measurements", len(recorded))
		}
		// Cancellation released the device slot: a new case can open.
		second, err := h.request(http.MethodPost, "/cases", map[string]any{
			"device_id": deviceID,
			"nominal":   25.0,
			"tolerance": 0.5,
			"steward":   "Qiao",
		})
		if err != nil {
			return err
		}
		if err := expectStatus(second, http.StatusCreated); err != nil {
			return err
		}
	case "review_ready", "recheck_required":
		if len(recorded) != 1 {
			return fmt.Errorf("measured case carries %d measurements, want 1", len(recorded))
		}
		// The measurement winner still occupies the device slot: a new case
		// for the device must be rejected.
		second, err := h.request(http.MethodPost, "/cases", map[string]any{
			"device_id": deviceID,
			"nominal":   25.0,
			"tolerance": 0.5,
			"steward":   "Qiao",
		})
		if err != nil {
			return err
		}
		if err := expectStatus(second, http.StatusConflict); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unexpected post-race state %q", finalState)
	}
	return nil
}

func checkSealVerification() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.readyCase("MET-1401")
	if err != nil {
		return err
	}
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer": fmt.Sprintf("Reviewer-%d", index+1),
				"summary":  "accepted measurement sealed for review",
			})
			results <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", successes, conflicts)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "sealed" {
		return fmt.Errorf("sealed case state = %q", state)
	}
	sealID, err := nestedString(read.Body, "seal", "id")
	if err != nil {
		return err
	}
	if sealID == "" {
		return errors.New("evidence seal is missing")
	}
	return nil
}

func newHarness() *harness {
	server := httptest.NewServer(bootstrap.NewHandler())
	return &harness{
		baseURL: server.URL,
		client:  server.Client(),
		server:  server,
	}
}

func (h *harness) close() {
	h.server.Close()
}

func (h *harness) request(method, path string, body any) (apiResponse, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return apiResponse{}, err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		return apiResponse{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.client.Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer response.Body.Close()
	rawBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return apiResponse{}, err
	}
	result := apiResponse{Status: response.StatusCode, Raw: string(rawBytes)}
	if len(bytes.TrimSpace(rawBytes)) > 0 {
		if err := json.Unmarshal(rawBytes, &result.Body); err != nil {
			return apiResponse{}, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return result, nil
}

func (h *harness) createDevice(tag string) (string, error) {
	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": tag,
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "device", "id")
}

func (h *harness) openCase(deviceID string) (apiResponse, error) {
	response, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) recordMeasurement(caseID string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed":   observed,
		"technician": technician,
	})
}

func (h *harness) cancelCase(caseID, actor, reason string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/cancel", map[string]any{
		"actor":  actor,
		"reason": reason,
	})
}

func (h *harness) rejectedCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 26.5, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func (h *harness) readyCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func (h *harness) sealedCase(tag string) (string, error) {
	caseID, err := h.readyCase(tag)
	if err != nil {
		return "", err
	}
	response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer": "Lin",
		"summary":  "accepted measurement sealed for review",
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func expectStatus(response apiResponse, expected int) error {
	if response.Status != expected {
		return fmt.Errorf("status = %d, expected %d: %s", response.Status, expected, response.Raw)
	}
	return nil
}

func expectErrorCode(response apiResponse, expected string) error {
	code, err := nestedString(response.Body, "error", "code")
	if err != nil {
		return err
	}
	if code != expected {
		return fmt.Errorf("error code = %q, expected %q", code, expected)
	}
	return nil
}

func nestedString(root map[string]any, path ...string) (string, error) {
	value, err := nestedValue(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", strings.Join(path, "."))
	}
	return text, nil
}

func nestedBool(root map[string]any, path ...any) (bool, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return false, err
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%v is not a boolean", path)
	}
	return flag, nil
}

func nestedSlice(root map[string]any, key string) ([]any, error) {
	value, ok := root[key]
	if !ok {
		return nil, fmt.Errorf("missing %s", key)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array", key)
	}
	return items, nil
}

func hasEvent(root map[string]any, kind string) bool {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return false
	}
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == kind {
			return true
		}
	}
	return false
}

func nestedValue(root map[string]any, path ...string) (any, error) {
	parts := make([]any, 0, len(path))
	for _, part := range path {
		parts = append(parts, part)
	}
	return nestedAny(root, parts...)
}

func nestedAny(root map[string]any, path ...any) (any, error) {
	var current any = root
	for _, part := range path {
		switch typed := part.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s is not an object", typed)
			}
			value, ok := object[typed]
			if !ok {
				return nil, fmt.Errorf("missing %s", typed)
			}
			current = value
		case int:
			items, ok := current.([]any)
			if !ok {
				return nil, fmt.Errorf("index %d is not inside an array", typed)
			}
			if typed < 0 || typed >= len(items) {
				return nil, fmt.Errorf("index %d is out of range", typed)
			}
			current = items[typed]
		default:
			return nil, fmt.Errorf("unsupported path element %T", part)
		}
	}
	return current, nil
}
