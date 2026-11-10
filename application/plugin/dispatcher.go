package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// The host-service wire protocol is a JSON envelope in both directions. The
// transport status only reports whether the call itself could be carried out;
// permission, validation, quota, and not-found failures travel in the envelope
// so every denial is typed and readable by the guest.
//
// Request bodies are method-specific JSON objects. Responses are:
//
//	{"code":"ok","data":{...}}
//	{"code":"denied","message":"...","permission":"clipboard.read","scope":{...}}
//
// The closed code set is ok, denied, not-found, conflict, invalid, limit, and
// fault. fault reports an unexpected host-side failure, never a caller error.

type serviceEnvelope struct {
	Code       string                  `json:"code"`
	Message    string                  `json:"message,omitempty"`
	Permission domainplugin.Permission `json:"permission,omitempty"`
	Scope      *domainplugin.Scope     `json:"scope,omitempty"`
	Data       json.RawMessage         `json:"data,omitempty"`
}

type serviceDispatcher struct {
	session *ServiceSession
}

// Call executes one host-service call. It always returns an envelope; the
// adapter maps only transport failures to nonzero statuses.
func (d serviceDispatcher) Call(ctx context.Context, call ServiceCall) (response []byte) {
	defer func() {
		if recovered := recover(); recovered != nil {
			response = faultEnvelope(fmt.Sprintf("host service panic: %v", recovered))
		}
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	if call.Service == ServiceUI {
		return d.uiCall(call)
	}
	if d.session == nil {
		return faultEnvelope(ErrServiceUnavailable.Error())
	}
	envelope := d.dispatch(ctx, call)
	payload, err := json.Marshal(envelope)
	if err != nil {
		return faultEnvelope("encode service response")
	}
	return payload
}

func (d serviceDispatcher) dispatch(ctx context.Context, call ServiceCall) serviceEnvelope {
	switch call.Service {
	case ServiceStorage:
		return d.storage(ctx, call)
	case ServiceClipboard:
		return d.clipboard(ctx, call)
	case ServiceNotifications:
		return d.notifications(ctx, call)
	case ServiceEvents:
		return d.events(ctx, call)
	case ServiceSettings:
		return d.settings(ctx, call)
	case ServiceSystemPanel:
		return d.systemPanel(ctx, call)
	case ServiceFiles:
		return d.files(ctx, call)
	case ServiceHosts:
		return d.hosts(ctx, call)
	case ServiceSessions:
		return d.sessions(ctx, call)
	case ServiceTerminal:
		return d.terminal(ctx, call)
	case ServiceServices:
		return d.services(ctx, call)
	case ServiceI18n:
		return d.i18n(ctx, call)
	case ServiceNetwork:
		return d.network(ctx, call)
	case ServiceAuth:
		return d.auth(ctx, call)
	case ServiceTrust:
		return d.trust(ctx, call)
	case ServiceVault:
		return d.vault(ctx, call)
	default:
		return invalidEnvelope(ServiceCall{Service: call.Service},
			fmt.Sprintf("unknown host service %q", call.Service))
	}
}

func (d serviceDispatcher) storage(_ context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.StorageMethodGet:
		var request struct {
			Key string `json:"key"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		value, err := d.session.StorageGet(request.Key)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]string{
			"value": base64.StdEncoding.EncodeToString(value),
		})
	case domainpluginabi.StorageMethodSet:
		var request struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		value, err := base64.StdEncoding.DecodeString(request.Value)
		if err != nil {
			return invalidEnvelope(call, "value is not valid base64")
		}
		if err := d.session.StorageSet(request.Key, value); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.StorageMethodDelete:
		var request struct {
			Key string `json:"key"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.StorageDelete(request.Key); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.StorageMethodList:
		var request struct {
			Prefix string `json:"prefix"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		keys, err := d.session.StorageList(request.Prefix)
		if err != nil {
			return errorEnvelope(call, err)
		}
		if keys == nil {
			keys = []string{}
		}
		return okEnvelope(map[string][]string{"keys": keys})
	default:
		return unknownMethodEnvelope(call)
	}
}

func (d serviceDispatcher) clipboard(ctx context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.ClipboardMethodRead:
		var request struct{}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		text, err := d.session.ClipboardRead(ctx)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]string{"text": text})
	case domainpluginabi.ClipboardMethodWrite:
		var request struct {
			Text string `json:"text"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.ClipboardWrite(ctx, request.Text); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	default:
		return unknownMethodEnvelope(call)
	}
}

func (d serviceDispatcher) notifications(ctx context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.NotificationsMethodPost:
		var request struct {
			Title   string               `json:"title"`
			Body    string               `json:"body"`
			Actions []NotificationAction `json:"actions"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		result, err := d.session.NotificationPost(ctx, request.Title, request.Body, request.Actions)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(result)
	case domainpluginabi.NotificationsMethodDismiss:
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.NotificationDismiss(request.ID); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	default:
		return unknownMethodEnvelope(call)
	}
}

func (d serviceDispatcher) events(_ context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.EventsMethodSubscribe:
		var request struct {
			Type string `json:"type"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := d.session.EventsSubscribe(request.Type)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]string{
			"handle": strconv.FormatUint(uint64(handle), 10),
		})
	case domainpluginabi.EventsMethodPoll:
		var request struct {
			Handle string `json:"handle"`
			Limit  int    `json:"limit"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		events, err := d.session.EventsPoll(handle, request.Limit)
		if err != nil {
			return errorEnvelope(call, err)
		}
		if events == nil {
			events = []HostEvent{}
		}
		return okEnvelope(map[string][]HostEvent{"events": events})
	case domainpluginabi.EventsMethodUnsubscribe:
		var request struct {
			Handle string `json:"handle"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.EventsUnsubscribe(handle); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	default:
		return unknownMethodEnvelope(call)
	}
}

func (d serviceDispatcher) settings(ctx context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.SettingsMethodRead:
		var request struct {
			Key string `json:"key"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		value, err := d.session.SettingsRead(ctx, request.Key)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]string{"value": value})
	default:
		return unknownMethodEnvelope(call)
	}
}

func (d serviceDispatcher) files(ctx context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.FilesMethodPreopens:
		var request struct{}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		preopens, err := d.session.FilePreopens()
		if err != nil {
			return errorEnvelope(call, err)
		}
		if preopens == nil {
			preopens = []FilePreopen{}
		}
		return okEnvelope(map[string][]FilePreopen{"preopens": preopens})
	case domainpluginabi.FilesMethodOpen:
		var request struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := d.session.FileOpen(request.Path, request.Mode)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]string{"handle": strconv.FormatUint(uint64(handle), 10)})
	case domainpluginabi.FilesMethodRead:
		var request struct {
			Handle string `json:"handle"`
			Offset int64  `json:"offset"`
			Length int    `json:"length"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		data, eof, err := d.session.FileRead(handle, request.Offset, request.Length)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{
			"data": base64.StdEncoding.EncodeToString(data),
			"eof":  eof,
		})
	case domainpluginabi.FilesMethodWrite:
		var request struct {
			Handle string `json:"handle"`
			Offset int64  `json:"offset"`
			Data   string `json:"data"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		data, err := base64.StdEncoding.DecodeString(request.Data)
		if err != nil {
			return invalidEnvelope(call, "data is not valid base64")
		}
		written, err := d.session.FileWrite(handle, request.Offset, data)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]int{"written": written})
	case domainpluginabi.FilesMethodTruncate:
		var request struct {
			Handle string `json:"handle"`
			Size   int64  `json:"size"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.FileTruncate(handle, request.Size); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.FilesMethodSync:
		var request struct {
			Handle string `json:"handle"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.FileSync(handle); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.FilesMethodStat:
		var request struct {
			Path string `json:"path"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		entry, err := d.session.FileStat(request.Path)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(entry)
	case domainpluginabi.FilesMethodRename:
		var request struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.FileRename(request.From, request.To); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.FilesMethodUnlink:
		var request struct {
			Path string `json:"path"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.FileUnlink(request.Path); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.FilesMethodClose:
		var request struct {
			Handle string `json:"handle"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.FileClose(handle); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.FilesMethodLock:
		var request struct {
			Handle    string `json:"handle"`
			Exclusive bool   `json:"exclusive"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.FileLock(handle, request.Exclusive); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.FilesMethodUnlock:
		var request struct {
			Handle string `json:"handle"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.FileUnlock(handle); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.FilesMethodList:
		var request struct {
			Path string `json:"path"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		entries, err := d.session.FileList(request.Path)
		if err != nil {
			return errorEnvelope(call, err)
		}
		if entries == nil {
			entries = []FileEntry{}
		}
		return okEnvelope(map[string][]FileEntry{"entries": entries})
	case domainpluginabi.FilesMethodRemoteList:
		var request struct {
			Host string `json:"host"`
			Path string `json:"path"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		entries, err := d.session.RemoteList(request.Host, request.Path)
		if err != nil {
			return errorEnvelope(call, err)
		}
		if entries == nil {
			entries = []RemoteEntry{}
		}
		return okEnvelope(map[string][]RemoteEntry{"entries": entries})
	case domainpluginabi.FilesMethodRemoteStat:
		var request struct {
			Host string `json:"host"`
			Path string `json:"path"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		entry, err := d.session.RemoteStat(request.Host, request.Path)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(entry)
	case domainpluginabi.FilesMethodRemoteRead:
		var request struct {
			Host   string `json:"host"`
			Path   string `json:"path"`
			Offset int64  `json:"offset"`
			Length int    `json:"length"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		data, eof, err := d.session.RemoteRead(request.Host, request.Path, request.Offset, request.Length)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{
			"data": base64.StdEncoding.EncodeToString(data),
			"eof":  eof,
		})
	case domainpluginabi.FilesMethodRemoteWrite:
		var request struct {
			Host   string `json:"host"`
			Path   string `json:"path"`
			Offset int64  `json:"offset"`
			Data   string `json:"data"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		data, err := base64.StdEncoding.DecodeString(request.Data)
		if err != nil {
			return invalidEnvelope(call, "data is not valid base64")
		}
		written, err := d.session.RemoteWrite(request.Host, request.Path, request.Offset, data)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]int{"written": written})
	case domainpluginabi.FilesMethodRemoteRename:
		var request struct {
			Host string `json:"host"`
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.RemoteRename(request.Host, request.From, request.To); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.FilesMethodRemoteDelete:
		var request struct {
			Host      string `json:"host"`
			Path      string `json:"path"`
			Recursive bool   `json:"recursive"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.RemoteDelete(request.Host, request.Path, request.Recursive); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.FilesMethodRemoteMkdir:
		var request struct {
			Host string `json:"host"`
			Path string `json:"path"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.RemoteMkdir(request.Host, request.Path); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	default:
		return unknownMethodEnvelope(call)
	}
}

// hostDraftRequest is the guest-facing Host draft. CreatedBy is deliberately
// absent: provenance is host-assigned.
type hostDraftRequest struct {
	Label    string   `json:"label"`
	Hostname string   `json:"hostname"`
	Port     int      `json:"port"`
	Protocol string   `json:"protocol"`
	Username string   `json:"username"`
	Tags     []string `json:"tags"`
}

func (r hostDraftRequest) hostDraft() HostDraft {
	return HostDraft{
		Label: r.Label, Hostname: r.Hostname, Port: r.Port,
		Protocol: r.Protocol, Username: r.Username,
		Tags: append([]string(nil), r.Tags...),
	}
}

func (d serviceDispatcher) hosts(_ context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.HostsMethodList:
		var request struct{}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		hosts, err := d.session.HostsList()
		if err != nil {
			return errorEnvelope(call, err)
		}
		if hosts == nil {
			hosts = []HostSummary{}
		}
		return okEnvelope(map[string][]HostSummary{"hosts": hosts})
	case domainpluginabi.HostsMethodGet:
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		host, err := d.session.HostsGet(request.ID)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]HostSummary{"host": host})
	case domainpluginabi.HostsMethodCreate:
		var request hostDraftRequest
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		host, err := d.session.HostsCreate(request.hostDraft())
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]HostSummary{"host": host})
	case domainpluginabi.HostsMethodUpdate:
		var request struct {
			ID       string    `json:"id"`
			Label    *string   `json:"label"`
			Hostname *string   `json:"hostname"`
			Port     *int      `json:"port"`
			Protocol *string   `json:"protocol"`
			Username *string   `json:"username"`
			Tags     *[]string `json:"tags"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		host, err := d.session.HostsUpdate(request.ID, HostPatch{
			Label: request.Label, Hostname: request.Hostname, Port: request.Port,
			Protocol: request.Protocol, Username: request.Username, Tags: request.Tags,
		})
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]HostSummary{"host": host})
	case domainpluginabi.HostsMethodImport:
		var request struct {
			Hosts []hostDraftRequest `json:"hosts"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		drafts := make([]HostDraft, 0, len(request.Hosts))
		for _, draft := range request.Hosts {
			drafts = append(drafts, draft.hostDraft())
		}
		hosts, err := d.session.HostsImport(drafts)
		if err != nil {
			return errorEnvelope(call, err)
		}
		if hosts == nil {
			hosts = []HostSummary{}
		}
		return okEnvelope(map[string][]HostSummary{"hosts": hosts})
	case domainpluginabi.HostsMethodTest:
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		result, err := d.session.HostsTest(request.ID)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{
			"reachable": result.Reachable,
			"latencyMs": result.LatencyMS,
			"detail":    result.Detail,
		})
	default:
		return unknownMethodEnvelope(call)
	}
}

func (d serviceDispatcher) sessions(_ context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.SessionsMethodList:
		var request struct{}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		sessions, err := d.session.SessionsList()
		if err != nil {
			return errorEnvelope(call, err)
		}
		if sessions == nil {
			sessions = []SessionSummary{}
		}
		return okEnvelope(map[string][]SessionSummary{"sessions": sessions})
	case domainpluginabi.SessionsMethodGet:
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		session, err := d.session.SessionsGet(request.ID)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]SessionSummary{"session": session})
	case domainpluginabi.SessionsMethodConnect:
		var request struct {
			HostID string `json:"hostId"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		session, err := d.session.SessionsConnect(request.HostID)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]SessionSummary{"session": session})
	case domainpluginabi.SessionsMethodReconnect:
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.SessionsReconnect(request.ID); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.SessionsMethodDisconnect:
		var request struct {
			ID string `json:"id"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.SessionsDisconnect(request.ID); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	default:
		return unknownMethodEnvelope(call)
	}
}

func (d serviceDispatcher) terminal(ctx context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.TerminalMethodObserve:
		var request struct {
			SessionID string `json:"sessionId"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := d.session.TerminalObserve(request.SessionID)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]string{"handle": strconv.FormatUint(uint64(handle), 10)})
	case domainpluginabi.TerminalMethodRead:
		var request struct {
			Handle string `json:"handle"`
			Limit  int    `json:"limit"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		data, dropped, closed, err := d.session.TerminalRead(ctx, handle, request.Limit)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{
			"data":    base64.StdEncoding.EncodeToString(data),
			"dropped": dropped,
			"closed":  closed,
		})
	case domainpluginabi.TerminalMethodClose:
		var request struct {
			Handle string `json:"handle"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.TerminalClose(handle); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.TerminalMethodWrite:
		var request struct {
			SessionID string `json:"sessionId"`
			Data      string `json:"data"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		data, err := base64.StdEncoding.DecodeString(request.Data)
		if err != nil {
			return invalidEnvelope(call, "data is not valid base64")
		}
		if err := d.session.TerminalWrite(request.SessionID, data); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.TerminalMethodInfo:
		var request struct {
			SessionID string `json:"sessionId"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		info, err := d.session.TerminalInfo(request.SessionID)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]TerminalInfo{"info": info})
	default:
		return unknownMethodEnvelope(call)
	}
}

func (d serviceDispatcher) systemPanel(ctx context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.SystempanelMethodSnapshot:
		var request struct {
			HostID     string                        `json:"host_id"`
			Collectors []domainplugin.PanelCollector `json:"collectors"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if request.HostID == "" {
			return invalidEnvelope(call, "host_id is required")
		}
		snapshot, err := d.session.PanelSnapshot(ctx, request.HostID, request.Collectors)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(snapshot)
	case domainpluginabi.SystempanelMethodUpdate:
		var request struct {
			HostID    string          `json:"host_id"`
			SectionID string          `json:"section_id"`
			Payload   json.RawMessage `json:"payload"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if request.HostID == "" || request.SectionID == "" {
			return invalidEnvelope(call, "host_id and section_id are required")
		}
		if len(request.Payload) == 0 {
			return invalidEnvelope(call, "payload is required")
		}
		if err := d.session.PanelUpdate(request.HostID, request.SectionID, request.Payload); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	default:
		return unknownMethodEnvelope(call)
	}
}

// i18n serves the permission-free i18n service. Every method reads the host
// locale and the catalogs loaded with the package; no method takes a locale
// argument because v1 has no per-plugin locale override.
func (d serviceDispatcher) i18n(_ context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.I18nMethodLocale:
		var request struct{}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		snapshot, err := d.session.Catalog()
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{
			"locale":     snapshot.Locale,
			"direction":  snapshot.Direction,
			"generation": snapshot.Generation,
		})
	case domainpluginabi.I18nMethodDirection:
		var request struct{}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		direction, err := d.session.Direction()
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]string{"direction": string(direction)})
	case domainpluginabi.I18nMethodTranslate:
		var request struct {
			Key     string            `json:"key"`
			Literal string            `json:"literal"`
			Args    map[string]string `json:"args"`
			Count   *int              `json:"count"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		var (
			translation MessageTranslation
			err         error
		)
		if request.Count != nil {
			translation, err = d.session.TranslatePlural(
				request.Key, request.Literal, request.Args, *request.Count)
		} else {
			translation, err = d.session.Translate(request.Key, request.Literal, request.Args)
		}
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(translation)
	case domainpluginabi.I18nMethodCatalog:
		var request struct{}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		snapshot, err := d.session.Catalog()
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(snapshot)
	case domainpluginabi.I18nMethodNumber:
		var request struct {
			Value float64 `json:"value"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		text, err := d.session.FormatNumber(request.Value)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]string{"text": text})
	case domainpluginabi.I18nMethodDate:
		var request struct {
			Value string `json:"value"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		value, err := parseServiceTime("value", request.Value)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		text, err := d.session.FormatDate(value)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]string{"text": text})
	case domainpluginabi.I18nMethodRelativeTime:
		var request struct {
			Value string `json:"value"`
			Now   string `json:"now"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		value, err := parseServiceTime("value", request.Value)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		now := d.session.host.now()
		if request.Now != "" {
			now, err = parseServiceTime("now", request.Now)
			if err != nil {
				return invalidEnvelope(call, err.Error())
			}
		}
		text, err := d.session.FormatRelativeTime(value, now)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]string{"text": text})
	case domainpluginabi.I18nMethodPlural:
		var request struct {
			Count int `json:"count"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		category, err := d.session.PluralCategory(request.Count)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]string{"category": category})
	default:
		return unknownMethodEnvelope(call)
	}
}

// parseServiceTime decodes one RFC 3339 timestamp field.
func parseServiceTime(field, raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, fmt.Errorf("%s is required and must be an RFC 3339 timestamp", field)
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be an RFC 3339 timestamp: %w", field, err)
	}
	return value, nil
}

func (d serviceDispatcher) network(ctx context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.NetworkMethodDial:
		var request struct {
			Host      string `json:"host"`
			Port      int    `json:"port"`
			Protocol  string `json:"protocol"`
			TLS       string `json:"tls"`
			DNS       string `json:"dns"`
			ViaHost   string `json:"via_host"`
			TimeoutMS int64  `json:"timeout_ms"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if request.TimeoutMS < 0 {
			return invalidEnvelope(call, "timeout_ms cannot be negative")
		}
		var timeout time.Duration
		if request.TimeoutMS > 0 {
			timeout = time.Duration(request.TimeoutMS) * time.Millisecond
		}
		result, err := d.session.NetworkDial(ctx, NetworkDialSpec{
			Host: request.Host, Port: request.Port, Protocol: request.Protocol,
			TLS: request.TLS, DNS: request.DNS, ViaHost: request.ViaHost,
			Timeout: timeout,
		})
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{
			"handle":   formatServiceHandle(result.Handle),
			"endpoint": result.Endpoint,
			"protocol": result.Protocol,
			"tls":      result.TLS,
			"resolved": result.Resolved,
			"via_host": result.ViaHost,
		})
	case domainpluginabi.NetworkMethodRead:
		var request struct {
			Handle   string `json:"handle"`
			MaxBytes int    `json:"max_bytes"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		result, err := d.session.NetworkRead(ctx, handle, request.MaxBytes)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{
			"data":        base64.StdEncoding.EncodeToString(result.Data),
			"credit":      result.Credit,
			"eof":         result.EOF,
			"would_block": result.WouldBlock,
		})
	case domainpluginabi.NetworkMethodWrite:
		var request struct {
			Handle string `json:"handle"`
			Data   string `json:"data"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		data, err := base64.StdEncoding.DecodeString(request.Data)
		if err != nil {
			return invalidEnvelope(call, "data is not valid base64")
		}
		result, err := d.session.NetworkWrite(ctx, handle, data)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{
			"written": result.Written,
			"credit":  result.Credit,
		})
	case domainpluginabi.NetworkMethodShutdown:
		var request struct {
			Handle    string `json:"handle"`
			Direction string `json:"direction"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.NetworkShutdown(handle, request.Direction); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.NetworkMethodClose:
		var request struct {
			Handle string `json:"handle"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.NetworkClose(handle); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	case domainpluginabi.NetworkMethodTlsUpgrade:
		var request struct {
			Handle string `json:"handle"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		result, err := d.session.NetworkTLSUpgrade(ctx, handle)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{
			"mode":        result.Mode,
			"server_name": result.ServerName,
		})
	default:
		return unknownMethodEnvelope(call)
	}
}

func (d serviceDispatcher) auth(ctx context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.AuthMethodBegin:
		var request struct {
			HostID string `json:"host_id"`
			Method string `json:"method"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		challenge, err := d.session.AuthBegin(ctx, request.HostID, AuthMethod(request.Method))
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{
			"challenge_id": challenge.ID,
			"host_id":      challenge.HostID,
			"method":       challenge.Method,
			"prompt":       challenge.Prompt,
			"can_respond":  challenge.CanRespond,
		})
	case domainpluginabi.AuthMethodRespond:
		var request struct {
			ChallengeID string `json:"challenge_id"`
			Answer      string `json:"answer"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		result, err := d.session.AuthRespond(ctx, request.ChallengeID, request.Answer)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{
			"challenge_id": result.ChallengeID,
			"status":       result.Status,
			"lease":        result.Lease,
		})
	case domainpluginabi.AuthMethodCancel:
		var request struct {
			ChallengeID string `json:"challenge_id"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.AuthCancel(ctx, request.ChallengeID); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	default:
		return unknownMethodEnvelope(call)
	}
}

func (d serviceDispatcher) trust(ctx context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.TrustMethodVerify:
		var request struct {
			HostID   string `json:"host_id"`
			Kind     string `json:"kind"`
			Material string `json:"material"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		material, err := base64.StdEncoding.DecodeString(request.Material)
		if err != nil {
			return invalidEnvelope(call, "material is not valid base64")
		}
		result, err := d.session.TrustVerify(ctx, request.HostID, TrustKind(request.Kind), material)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{
			"verdict":     result.Verdict,
			"fingerprint": result.Fingerprint,
			"key_type":    result.KeyType,
		})
	default:
		return unknownMethodEnvelope(call)
	}
}

func (d serviceDispatcher) vault(ctx context.Context, call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.VaultMethodMetadata:
		var request struct {
			HostID string `json:"host_id"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		descriptors, err := d.session.VaultMetadata(ctx, request.HostID)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{"fields": descriptors})
	case domainpluginabi.VaultMethodFields:
		var request struct {
			HostID string `json:"host_id"`
			Field  string `json:"field"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		descriptor, err := d.session.VaultFields(ctx, request.HostID, request.Field)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{"field": descriptor})
	case domainpluginabi.VaultMethodLeaseIssue:
		var request struct {
			HostID     string `json:"host_id"`
			Field      string `json:"field"`
			TTLMS      int64  `json:"ttl_ms"`
			Connection string `json:"connection"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if request.TTLMS < 0 {
			return invalidEnvelope(call, "ttl_ms cannot be negative")
		}
		var connection Handle
		if request.Connection != "" {
			handle, err := parseServiceHandle(request.Connection)
			if err != nil {
				return invalidEnvelope(call, err.Error())
			}
			connection = handle
		}
		var ttl time.Duration
		if request.TTLMS > 0 {
			ttl = time.Duration(request.TTLMS) * time.Millisecond
		}
		lease, err := d.session.VaultLeaseIssue(ctx, request.HostID, request.Field, ttl, connection)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{
			"handle":     formatServiceHandle(lease.Handle),
			"host_id":    lease.HostID,
			"field":      lease.Field,
			"expires_at": lease.ExpiresAt.UTC().Format(time.RFC3339Nano),
			"connection": formatOptionalHandle(lease.Connection),
		})
	case domainpluginabi.VaultMethodLeaseRead:
		var request struct {
			Handle string `json:"handle"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		value, err := d.session.VaultLeaseRead(ctx, handle)
		if err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{
			"value":      base64.StdEncoding.EncodeToString(value.Value),
			"expires_at": value.ExpiresAt.UTC().Format(time.RFC3339Nano),
		})
	case domainpluginabi.VaultMethodLeaseRelease:
		var request struct {
			Handle string `json:"handle"`
		}
		if err := decodeServiceRequest(call, &request); err != nil {
			return invalidEnvelope(call, err.Error())
		}
		handle, err := parseServiceHandle(request.Handle)
		if err != nil {
			return invalidEnvelope(call, err.Error())
		}
		if err := d.session.VaultLeaseRelease(ctx, handle); err != nil {
			return errorEnvelope(call, err)
		}
		return okEnvelope(map[string]any{})
	default:
		return unknownMethodEnvelope(call)
	}
}

func formatServiceHandle(handle Handle) string {
	return strconv.FormatUint(uint64(handle), 10)
}

func formatOptionalHandle(handle Handle) string {
	if handle == 0 {
		return ""
	}
	return formatServiceHandle(handle)
}
func decodeServiceRequest(call ServiceCall, target any) error {
	if len(call.Request) == 0 {
		return nil
	}
	if err := json.Unmarshal(call.Request, target); err != nil {
		return fmt.Errorf("request is not a valid JSON object: %w", err)
	}
	return nil
}

func parseServiceHandle(raw string) (Handle, error) {
	if raw == "" {
		return 0, errors.New("a service handle is required")
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.New("service handle is not a decimal u64")
	}
	return Handle(value), nil
}

func okEnvelope(data any) serviceEnvelope {
	if data == nil {
		data = map[string]any{}
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return faultServiceEnvelope("encode service payload")
	}
	return serviceEnvelope{Code: ServiceCodeOK, Data: payload}
}

func errorEnvelope(call ServiceCall, err error) serviceEnvelope {
	var service *ServiceError
	if errors.As(err, &service) {
		envelope := serviceEnvelope{
			Code:       service.Code,
			Message:    service.Message,
			Permission: service.Permission,
		}
		if !service.Scope.IsZero() {
			scope := service.Scope
			envelope.Scope = &scope
		}
		return envelope
	}
	return faultServiceEnvelope(err.Error())
}

func faultServiceEnvelope(message string) serviceEnvelope {
	return serviceEnvelope{Code: ServiceCodeFault, Message: message}
}

func invalidEnvelope(call ServiceCall, message string) serviceEnvelope {
	return serviceEnvelope{Code: ServiceCodeInvalid, Message: message}
}

func unknownMethodEnvelope(call ServiceCall) serviceEnvelope {
	return invalidEnvelope(call, fmt.Sprintf("unknown method %d for service %q",
		call.Method, call.Service))
}

func faultEnvelope(message string) []byte {
	payload, _ := json.Marshal(faultServiceEnvelope(message))
	return payload
}
