package plugin

import (
	"errors"
	"fmt"

	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// uiCall executes one native UI service call. Unlike the baseline services,
// the payload is the binary retained-tree encoding generated from the IDL:
// permission denials and validation failures travel in the binary status
// envelope, never in the JSON transport envelope.
func (d serviceDispatcher) uiCall(call ServiceCall) (response []byte) {
	defer func() {
		if recovered := recover(); recovered != nil {
			response = domainnativeui.EncodeStatus(domainnativeui.StatusResponse{
				Status:  domainnativeui.StatusUnavailable,
				Message: fmt.Sprintf("native ui host failure: %v", recovered),
			})
		}
	}()
	if d.session == nil {
		return uiStatus(domainnativeui.StatusUnavailable, ErrServiceUnavailable.Error())
	}
	ui := d.session.NativeUI()
	if ui == nil {
		return uiStatus(domainnativeui.StatusUnavailable, "native ui is not configured")
	}
	method, ok := uiMethodName(call.Method)
	if !ok {
		return uiStatus(domainnativeui.StatusInvalid,
			fmt.Sprintf("unknown method %d for service %q", call.Method, call.Service))
	}
	if call.Method == domainpluginabi.UiMethodWebviewRespond {
		return d.webviewRespond(call)
	}
	if !uiNativeMethod(call.Method) {
		return uiStatus(domainnativeui.StatusUnavailable,
			fmt.Sprintf("ui.%s is not served by this host", method))
	}

	switch call.Method {
	case domainpluginabi.UiMethodApply:
		if denied := d.uiDenied(method, domainplugin.PermissionUINative); denied != nil {
			return denied
		}
		batch, err := domainnativeui.DecodeBatch(call.Request)
		if err != nil {
			return uiStatus(domainnativeui.StatusInvalid, err.Error())
		}
		result, err := ui.Apply(batch)
		if err != nil {
			return uiStatus(UIStatusOf(err), err.Error())
		}
		return domainnativeui.EncodeStatus(domainnativeui.StatusResponse{
			Status:   domainnativeui.StatusOK,
			Revision: result.Revision,
			Created:  result.Created,
		})
	case domainpluginabi.UiMethodListen:
		request, err := domainnativeui.DecodeListenRequest(call.Request)
		if err != nil {
			return domainnativeui.EncodeListenResponse(domainnativeui.ListenResponse{
				Status: domainnativeui.StatusInvalid, Message: err.Error(),
			})
		}
		// The session-wide webview event is the page bridge's inbound path.
		// A webview-only plugin must be able to subscribe to it with the
		// ui.webview grant, without asking for the native tree permission.
		permission := domainplugin.PermissionUINative
		if request.Event == domainnativeui.EventWebview {
			permission = domainplugin.PermissionUIWebview
		}
		if denied := d.uiDenied(method, permission); denied != nil {
			return denied
		}
		handle, err := ui.Listen(request.Node, request.Event)
		if err != nil {
			return domainnativeui.EncodeListenResponse(domainnativeui.ListenResponse{
				Status: UIStatusOf(err), Message: err.Error(),
			})
		}
		return domainnativeui.EncodeListenResponse(domainnativeui.ListenResponse{
			Status: domainnativeui.StatusOK, Listener: uint64(handle),
		})
	case domainpluginabi.UiMethodUnlisten:
		if denied := d.uiDenied(method, d.uiEventPipelinePermission()); denied != nil {
			return denied
		}
		handle, err := domainnativeui.DecodeUnlistenRequest(call.Request)
		if err != nil {
			return uiStatus(domainnativeui.StatusInvalid, err.Error())
		}
		if err := ui.Unlisten(Handle(handle)); err != nil {
			return uiStatus(UIStatusOf(err), err.Error())
		}
		return uiStatus(domainnativeui.StatusOK, "")
	case domainpluginabi.UiMethodPoll:
		if denied := d.uiDenied(method, d.uiEventPipelinePermission()); denied != nil {
			return denied
		}
		max, err := domainnativeui.DecodePollRequest(call.Request)
		if err != nil {
			return domainnativeui.EncodePollResponse(domainnativeui.PollResponse{
				Status: domainnativeui.StatusInvalid, Message: err.Error(),
			})
		}
		return domainnativeui.EncodePollResponse(domainnativeui.PollResponse{
			Status: domainnativeui.StatusOK,
			Events: ui.Poll(max),
		})
	}
	return uiStatus(domainnativeui.StatusInvalid, "unhandled native ui method")
}

// uiDenied returns a binary denial response, or nil when the permission is
// held. Denials are audited by ServiceSession.check.
func (d serviceDispatcher) uiDenied(method string, permission domainplugin.Permission) []byte {
	if err := d.session.check(ServiceUI, method, permission, domainplugin.Scope{}); err != nil {
		status := domainnativeui.StatusDenied
		if !IsDenied(err) {
			status = domainnativeui.StatusUnavailable
		}
		return uiStatus(status, err.Error())
	}
	return nil
}

// uiEventPipelinePermission names the grant that lets the guest drain queued
// events. A native plugin uses ui.native; a webview-only plugin uses
// ui.webview for the webview event pipeline.
func (d serviceDispatcher) uiEventPipelinePermission() domainplugin.Permission {
	if d.session.holds(domainplugin.PermissionUINative, domainplugin.Scope{}) {
		return domainplugin.PermissionUINative
	}
	return domainplugin.PermissionUIWebview
}

// uiMethodName maps a stable method id to its IDL name for audit rows.
func uiMethodName(method uint32) (string, bool) {
	switch method {
	case domainpluginabi.UiMethodOpenView:
		return "open_view", true
	case domainpluginabi.UiMethodClose:
		return "close", true
	case domainpluginabi.UiMethodFocus:
		return "focus", true
	case domainpluginabi.UiMethodRestore:
		return "restore", true
	case domainpluginabi.UiMethodDialog:
		return "dialog", true
	case domainpluginabi.UiMethodTheme:
		return "theme", true
	case domainpluginabi.UiMethodLocale:
		return "locale", true
	case domainpluginabi.UiMethodApply:
		return "apply", true
	case domainpluginabi.UiMethodListen:
		return "listen", true
	case domainpluginabi.UiMethodUnlisten:
		return "unlisten", true
	case domainpluginabi.UiMethodPoll:
		return "poll", true
	case domainpluginabi.UiMethodWebviewRespond:
		return "webview_respond", true
	}
	return "", false
}

// uiNativeMethod reports whether the method is part of the ticket 11 native UI
// core. The view-management ids stay reserved until the placements work.
func uiNativeMethod(method uint32) bool {
	switch method {
	case domainpluginabi.UiMethodApply, domainpluginabi.UiMethodListen,
		domainpluginabi.UiMethodUnlisten, domainpluginabi.UiMethodPoll:
		return true
	}
	return false
}

// webviewRespond completes one page bridge request. It is the only ui method
// served under ui.webview instead of ui.native: the page itself is never a
// permission principal, and the backend may only answer a request the host
// previously delivered to it.
func (d serviceDispatcher) webviewRespond(call ServiceCall) []byte {
	if d.session == nil {
		return uiStatus(domainnativeui.StatusUnavailable, ErrServiceUnavailable.Error())
	}
	webview := d.session.Webview()
	if webview == nil {
		return uiStatus(domainnativeui.StatusUnavailable, ErrWebviewUnavailable.Error())
	}
	if err := d.session.check(ServiceUI, "webview_respond",
		domainplugin.PermissionUIWebview, domainplugin.Scope{}); err != nil {
		status := domainnativeui.StatusDenied
		if !IsDenied(err) {
			status = domainnativeui.StatusUnavailable
		}
		return uiStatus(status, err.Error())
	}
	reply, err := domainnativeui.DecodeWebviewReply(call.Request)
	if err != nil {
		return uiStatus(domainnativeui.StatusInvalid, err.Error())
	}
	if err := webview.Reply(reply); err != nil {
		switch {
		case errors.Is(err, ErrWebviewClosed):
			return uiStatus(domainnativeui.StatusClosed, err.Error())
		case errors.Is(err, ErrWebviewRequest):
			return uiStatus(domainnativeui.StatusStaleHandle, err.Error())
		case errors.Is(err, ErrWebviewPayload):
			return uiStatus(domainnativeui.StatusLimit, err.Error())
		case errors.Is(err, ErrWebviewUnavailable):
			return uiStatus(domainnativeui.StatusUnavailable, err.Error())
		}
		return uiStatus(domainnativeui.StatusInvalid, err.Error())
	}
	return uiStatus(domainnativeui.StatusOK, "")
}

func uiStatus(status domainnativeui.Status, message string) []byte {
	return domainnativeui.EncodeStatus(domainnativeui.StatusResponse{Status: status, Message: message})
}
