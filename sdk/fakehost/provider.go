package fakehost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// fakeProviderQueueBytes mirrors the production per-direction stream bound
// closely enough for fixture traffic while keeping exhaustion deterministic.
const fakeProviderQueueBytes = 64 << 10

func (r *Runner) providerCallbacks() *appplugin.ProviderCallbacks {
	return &appplugin.ProviderCallbacks{
		InputRead: func(stream uint32, capacity int) []byte {
			r.mu.Lock()
			defer r.mu.Unlock()
			queued := r.providerInput[stream]
			if capacity <= 0 || len(queued) == 0 {
				return nil
			}
			if capacity > len(queued) {
				capacity = len(queued)
			}
			out := append([]byte(nil), queued[:capacity]...)
			r.providerInput[stream] = append([]byte(nil), queued[capacity:]...)
			return out
		},
		InputCredit: func(stream uint32) int {
			r.mu.Lock()
			defer r.mu.Unlock()
			return len(r.providerInput[stream])
		},
		OutputWrite: func(stream uint32, data []byte) int32 {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.providerClosed[stream] {
				return -3
			}
			if len(r.providerOutput[stream])+len(data) > fakeProviderQueueBytes {
				return -2
			}
			r.providerOutput[stream] = append(r.providerOutput[stream], data...)
			return 0
		},
		OutputCredit: func(stream uint32) int {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.providerClosed[stream] {
				return 0
			}
			return fakeProviderQueueBytes - len(r.providerOutput[stream])
		},
		Cancelled: func(uint64) bool { return false },
	}
}

// ProviderCall runs one host-to-guest connection-provider request against the
// fixture stream queues.
func (r *Runner) ProviderCall(ctx context.Context, call ProviderCall) (ProviderObservation, error) {
	if r == nil || r.instance == nil {
		return ProviderObservation{}, fmt.Errorf("fake host: no live instance")
	}
	r.beginCall()
	defer r.endCall()
	method := domainpluginabi.ProviderMethod(call.Method)
	if !method.Valid() {
		return ProviderObservation{}, fmt.Errorf("unknown provider method %d", call.Method)
	}
	invoker, ok := r.instance.(appplugin.ProviderCapableInstance)
	if !ok || !invoker.SupportsProvider() {
		return ProviderObservation{}, fmt.Errorf("module does not export %s",
			domainpluginabi.ExportProviderRequest)
	}
	payload := []byte(call.Payload)
	if method == domainpluginabi.ProviderMethodPump && len(payload) == 0 && call.Stream != 0 {
		payload = []byte(fmt.Sprintf(`{"stream":%d}`, call.Stream))
	}
	if len(call.Input) > 0 && call.Stream != 0 {
		r.mu.Lock()
		r.providerInput[call.Stream] = append(r.providerInput[call.Stream], call.Input...)
		r.mu.Unlock()
	}
	r.mu.Lock()
	r.nextProviderID++
	requestID := r.nextProviderID
	r.mu.Unlock()
	response, err := invoker.InvokeProvider(ctx, appplugin.ProviderRequest{
		ID: requestID, Method: method, Payload: payload,
	})
	observation := ProviderObservation{Method: method.String(), Status: "ok"}
	if err != nil {
		observation.Status = "error"
		observation.Error = err.Error()
		r.recordProvider(observation)
		return observation, err
	}
	observation.Status, observation.Message = providerStatus(response)
	if observation.Status != "ok" {
		r.recordProvider(observation)
		return observation, fmt.Errorf("provider %s: %s", method, observation.Message)
	}
	if method == domainpluginabi.ProviderMethodOpen {
		var opened struct {
			Cols    int    `json:"cols"`
			Rows    int    `json:"rows"`
			Restore string `json:"restore"`
		}
		if err := json.Unmarshal(response, &opened); err != nil {
			observation.Status = "error"
			observation.Error = "malformed open response"
			r.recordProvider(observation)
			return observation, fmt.Errorf("provider open response: %w", err)
		}
		observation.Cols = opened.Cols
		observation.Rows = opened.Rows
		if opened.Restore != "" {
			restore, err := base64.StdEncoding.DecodeString(opened.Restore)
			if err != nil {
				observation.Status = "error"
				observation.Error = "malformed restore descriptor"
				r.recordProvider(observation)
				return observation, fmt.Errorf("provider restore: %w", err)
			}
			observation.Restore = restore
		}
	}
	if method == domainpluginabi.ProviderMethodClose && call.Stream != 0 {
		r.mu.Lock()
		r.providerClosed[call.Stream] = true
		r.mu.Unlock()
	}
	if method == domainpluginabi.ProviderMethodPump || call.ReadOutput {
		r.mu.Lock()
		observation.Output = append([]byte(nil), r.providerOutput[call.Stream]...)
		r.providerOutput[call.Stream] = nil
		r.mu.Unlock()
	}
	r.recordProvider(observation)
	return observation, nil
}

func (r *Runner) recordProvider(observation ProviderObservation) {
	r.mu.Lock()
	r.provider = append(r.provider, observation)
	r.mu.Unlock()
}

func providerStatus(response []byte) (string, string) {
	var envelope struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return "error", "malformed response"
	}
	if envelope.Status == "" {
		return "error", "response has no status"
	}
	return envelope.Status, envelope.Message
}
