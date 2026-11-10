package fakehost

import (
	"context"
	"encoding/json"
	"fmt"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// DataCall runs one host-to-guest data-provider request.
func (r *Runner) DataCall(ctx context.Context, call DataCall) (DataObservation, error) {
	if r == nil || r.instance == nil {
		return DataObservation{}, fmt.Errorf("fake host: no live instance")
	}
	r.beginCall()
	defer r.endCall()
	method := domainpluginabi.DataProviderMethod(call.Method)
	if !method.Valid() {
		return DataObservation{}, fmt.Errorf("unknown data provider method %d", call.Method)
	}
	invoker, ok := r.instance.(appplugin.DataProviderCapableInstance)
	if !ok || !invoker.SupportsDataProvider() {
		return DataObservation{}, fmt.Errorf("module does not export %s",
			domainpluginabi.ExportDataProviderRequest)
	}
	r.mu.Lock()
	r.nextDataID++
	requestID := r.nextDataID
	if call.CancelAfter > 0 {
		r.cancelData[requestID] = call.CancelAfter
	}
	r.mu.Unlock()
	response, err := invoker.InvokeDataProvider(ctx, appplugin.DataProviderRequest{
		ID: requestID, Method: method, Payload: []byte(call.Payload),
	})
	observation := dataObservation(method.String(), response)
	if err != nil {
		observation.Status = "error"
		observation.Error = err.Error()
		r.recordData(observation)
		return observation, err
	}
	if observation.Status != "ok" {
		r.recordData(observation)
		return observation, fmt.Errorf("data provider %s: %s", method, observation.Message)
	}
	r.recordData(observation)
	return observation, nil
}

// consumeCancellation decrements one request's remaining poll count. A value
// of zero means the request is never cancelled.
func consumeCancellation(cancels map[uint64]int, requestID uint64) bool {
	remaining := cancels[requestID]
	if remaining <= 0 {
		return false
	}
	remaining--
	cancels[requestID] = remaining
	return remaining == 0
}

func (r *Runner) recordData(observation DataObservation) {
	r.mu.Lock()
	r.data = append(r.data, observation)
	r.mu.Unlock()
}

// dataObservation normalizes one provider response into the fields the
// conformance comparison shares with the real DataProviderHost.
func dataObservation(method string, response []byte) DataObservation {
	observation := DataObservation{Method: method}
	var envelope struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		More    bool   `json:"more"`
		Partial bool   `json:"partial"`
		Class   string `json:"class"`
		Result  *struct {
			Kind    string `json:"kind"`
			Class   string `json:"class"`
			More    bool   `json:"more"`
			Partial bool   `json:"partial"`
			Table   *struct {
				Columns []struct {
					Name string `json:"name"`
				} `json:"columns"`
				Rows [][]json.RawMessage `json:"rows"`
			} `json:"table"`
			Summary *struct {
				AffectedRows int64 `json:"affectedRows"`
			} `json:"summary"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		observation.Status = "error"
		observation.Error = "malformed response"
		return observation
	}
	observation.Status = envelope.Status
	observation.Message = envelope.Message
	observation.More = envelope.More
	observation.Partial = envelope.Partial
	observation.Class = envelope.Class
	if envelope.Result != nil {
		observation.Kind = envelope.Result.Kind
		if observation.Class == "" {
			observation.Class = envelope.Result.Class
		}
		observation.More = observation.More || envelope.Result.More
		observation.Partial = observation.Partial || envelope.Result.Partial
		if envelope.Result.Table != nil {
			for _, column := range envelope.Result.Table.Columns {
				observation.Columns = append(observation.Columns, column.Name)
			}
			observation.Rows = len(envelope.Result.Table.Rows)
		}
		if envelope.Result.Summary != nil {
			observation.AffectedRows = envelope.Result.Summary.AffectedRows
		}
	}
	return observation
}
