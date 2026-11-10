package plugin_test

import (
	"context"
	"testing"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

func dispatchUI(t *testing.T, fixture *serviceFixture, method uint32, request []byte) []byte {
	t.Helper()
	return fixture.session.Dispatcher().Call(context.Background(),
		appplugin.AbiServiceCall(appplugin.ServiceUI, method, request))
}

func decodeUIStatus(t *testing.T, payload []byte) domainnativeui.StatusResponse {
	t.Helper()
	response, err := domainnativeui.DecodeStatus(payload)
	if err != nil {
		t.Fatalf("DecodeStatus: %v", err)
	}
	return response
}

// uiCounterTree builds the guest half of the sample counter view: a column
// with a status text, an input, and a button.
func uiCounterTree() domainnativeui.Batch {
	return domainnativeui.Batch{
		Root: domainnativeui.IDFromNode(1),
		Ops: []domainnativeui.Op{
			{Kind: domainnativeui.OpCreate, ID: 1, NodeKind: domainnativeui.NodeContainer},
			{Kind: domainnativeui.OpCreate, ID: 2, NodeKind: domainnativeui.NodeText},
			{Kind: domainnativeui.OpCreate, ID: 3, NodeKind: domainnativeui.NodeInput},
			{Kind: domainnativeui.OpCreate, ID: 4, NodeKind: domainnativeui.NodeButton},
			{Kind: domainnativeui.OpSetProp, Node: domainnativeui.IDFromNode(1), Prop: domainnativeui.PropLayout, Value: domainnativeui.IntValue(int64(domainnativeui.LayoutColumn))},
			{Kind: domainnativeui.OpSetText, Node: domainnativeui.IDFromNode(2), Text: "Count: 0"},
			{Kind: domainnativeui.OpSetProp, Node: domainnativeui.IDFromNode(3), Prop: domainnativeui.PropLabel, Value: domainnativeui.StringValue("Name")},
			{Kind: domainnativeui.OpSetProp, Node: domainnativeui.IDFromNode(4), Prop: domainnativeui.PropLabel, Value: domainnativeui.StringValue("Increment")},
			{Kind: domainnativeui.OpSetChildren, Node: domainnativeui.IDFromNode(1),
				Children: []domainnativeui.NodeRef{
					domainnativeui.IDFromNode(2),
					domainnativeui.IDFromNode(3),
					domainnativeui.IDFromNode(4),
				}},
		},
	}
}

func TestNativeUIServiceAppliesBatchesAndRejectsInvalidOnes(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	fixture.grant(domainplugin.PermissionUINative, domainplugin.Scope{})

	response := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodApply, domainnativeui.EncodeBatch(uiCounterTree())))
	if response.Status != domainnativeui.StatusOK || response.Revision != 1 {
		t.Fatalf("apply = %+v", response)
	}
	if len(response.Created) != 4 {
		t.Fatalf("created = %+v", response.Created)
	}
	ui := fixture.session.NativeUI()
	if got := ui.Tree().Nodes[2].Text; got != "Count: 0" {
		t.Fatalf("text = %q", got)
	}
	var buttonHandle uint64
	var textHandle uint64
	for _, created := range response.Created {
		switch created.ID {
		case 4:
			buttonHandle = created.Handle
		case 2:
			textHandle = created.Handle
		}
	}

	// A batch that sets a text-only node's intrusive property is rejected and
	// leaves the previous tree and handles intact.
	invalid := domainnativeui.Batch{Ops: []domainnativeui.Op{
		{Kind: domainnativeui.OpSetText, Node: domainnativeui.HandleFromNode(textHandle), Text: "Count: 1"},
		{Kind: domainnativeui.OpSetProp, Node: domainnativeui.HandleFromNode(textHandle),
			Prop: domainnativeui.PropSelected, Value: domainnativeui.IntValue(1)},
	}}
	rejected := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodApply, domainnativeui.EncodeBatch(invalid)))
	if rejected.Status != domainnativeui.StatusInvalid {
		t.Fatalf("invalid batch status = %v", rejected.Status)
	}
	if ui.Revision() != 1 || ui.Tree().Nodes[2].Text != "Count: 0" {
		t.Fatalf("rejected batch changed the tree: revision %d text %q",
			ui.Revision(), ui.Tree().Nodes[2].Text)
	}

	// The button handle still works, and a fabricated handle is stale.
	valid := domainnativeui.Batch{Ops: []domainnativeui.Op{
		{Kind: domainnativeui.OpSetText, Node: domainnativeui.IDFromNode(2), Text: "Count: 1"},
	}}
	if got := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodApply, domainnativeui.EncodeBatch(valid))); got.Status != domainnativeui.StatusOK {
		t.Fatalf("second apply = %+v", got)
	}
	stale := domainnativeui.Batch{Ops: []domainnativeui.Op{
		{Kind: domainnativeui.OpSetProp, Node: domainnativeui.HandleFromNode(0x1deadbeef),
			Prop: domainnativeui.PropLabel, Value: domainnativeui.StringValue("x")},
	}}
	if got := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodApply, domainnativeui.EncodeBatch(stale))); got.Status != domainnativeui.StatusStaleHandle {
		t.Fatalf("stale apply = %+v", got)
	}

	// Destroying the button releases its node handle.
	destroy := domainnativeui.Batch{Ops: []domainnativeui.Op{
		{Kind: domainnativeui.OpDestroy, Node: domainnativeui.HandleFromNode(buttonHandle)},
	}}
	if got := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodApply, domainnativeui.EncodeBatch(destroy))); got.Status != domainnativeui.StatusOK {
		t.Fatalf("destroy = %+v", got)
	}
	if _, ok := ui.NodeHandle(4); ok {
		t.Fatal("destroyed node still has a live handle")
	}
	if got := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodApply, domainnativeui.EncodeBatch(domainnativeui.Batch{Ops: []domainnativeui.Op{
			{Kind: domainnativeui.OpSetProp, Node: domainnativeui.HandleFromNode(buttonHandle),
				Prop: domainnativeui.PropLabel, Value: domainnativeui.StringValue("x")},
		}}))); got.Status != domainnativeui.StatusStaleHandle {
		t.Fatalf("destroyed handle = %+v", got)
	}

	// Reserved view-management methods are not served yet, and unknown method
	// ids are invalid rather than silently accepted.
	if got := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodOpenView, nil)); got.Status != domainnativeui.StatusUnavailable {
		t.Fatalf("open_view = %+v", got)
	}
	if got := decodeUIStatus(t, dispatchUI(t, fixture, 99, nil)); got.Status != domainnativeui.StatusInvalid {
		t.Fatalf("unknown method = %+v", got)
	}
}

func TestNativeUIDeniedWithoutThePermission(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	response := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodApply, domainnativeui.EncodeBatch(uiCounterTree())))
	if response.Status != domainnativeui.StatusDenied {
		t.Fatalf("status = %v, want denied", response.Status)
	}
	if fixture.session.NativeUI().Tree().Len() != 0 {
		t.Fatal("a denied apply built a tree")
	}
	if len(fixture.audit.events) == 0 {
		t.Fatal("denial was not audited")
	}
}

func TestNativeUIListenersFilterCoalesceAndPoll(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	fixture.grant(domainplugin.PermissionUINative, domainplugin.Scope{})
	applied := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodApply, domainnativeui.EncodeBatch(uiCounterTree())))
	ui := fixture.session.NativeUI()
	var inputHandle, textHandle uint64
	for _, created := range applied.Created {
		switch created.ID {
		case 3:
			inputHandle = created.Handle
		case 2:
			textHandle = created.Handle
		}
	}

	listen := func(node uint64, kind domainnativeui.EventKind) uint64 {
		t.Helper()
		payload := domainnativeui.EncodeListenRequest(domainnativeui.ListenRequest{
			Node: domainnativeui.HandleFromNode(node), Event: kind,
		})
		response, err := domainnativeui.DecodeListenResponse(
			dispatchUI(t, fixture, domainpluginabi.UiMethodListen, payload))
		if err != nil {
			t.Fatalf("DecodeListenResponse: %v", err)
		}
		if response.Status != domainnativeui.StatusOK {
			t.Fatalf("listen = %+v", response)
		}
		return response.Listener
	}
	inputListener := listen(inputHandle, domainnativeui.EventInput)
	listen(textHandle, domainnativeui.EventClick)

	if !ui.EnqueueNode(3, domainnativeui.Event{Kind: domainnativeui.EventInput, Text: "a"}) {
		t.Fatal("input event was not queued")
	}
	if !ui.EnqueueNode(3, domainnativeui.Event{Kind: domainnativeui.EventInput, Text: "ab"}) {
		t.Fatal("coalesced input event was not queued")
	}
	// No listener for this node and kind: the host drops it immediately.
	if ui.EnqueueNode(3, domainnativeui.Event{Kind: domainnativeui.EventHover}) {
		t.Fatal("event without a listener was queued")
	}
	if !ui.EnqueueNode(2, domainnativeui.Event{Kind: domainnativeui.EventClick}) {
		t.Fatal("click event was not queued")
	}

	events, err := domainnativeui.DecodePollResponse(
		dispatchUI(t, fixture, domainpluginabi.UiMethodPoll, domainnativeui.EncodePollRequest(0)))
	if err != nil {
		t.Fatalf("DecodePollResponse: %v", err)
	}
	if events.Status != domainnativeui.StatusOK || len(events.Events) != 2 {
		t.Fatalf("poll = %+v", events)
	}
	if events.Events[0].Kind != domainnativeui.EventInput || events.Events[0].Text != "ab" {
		t.Fatalf("coalesced event = %+v", events.Events[0])
	}
	if events.Events[0].Node != domainnativeui.HandleFromNode(inputHandle) {
		t.Fatalf("event node = %#x, want input handle %#x", uint64(events.Events[0].Node), inputHandle)
	}
	if events.Events[1].Kind != domainnativeui.EventClick ||
		events.Events[1].Node != domainnativeui.HandleFromNode(textHandle) {
		t.Fatalf("second event = %+v", events.Events[1])
	}

	// Unlisten releases the handle; a second unlisten is stale.
	if got := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodUnlisten, domainnativeui.EncodeUnlistenRequest(inputListener))); got.Status != domainnativeui.StatusOK {
		t.Fatalf("unlisten = %+v", got)
	}
	if got := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodUnlisten, domainnativeui.EncodeUnlistenRequest(inputListener))); got.Status != domainnativeui.StatusStaleHandle {
		t.Fatalf("second unlisten = %+v", got)
	}
}

// A drag, drop, or resize event names the other node in Related, and the guest
// only ever sees a generation-tagged handle there, never a raw guest id.
func TestNativeUIEventRelatedNodeIsAGenerationTaggedHandle(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	fixture.grant(domainplugin.PermissionUINative, domainplugin.Scope{})
	applied := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodApply, domainnativeui.EncodeBatch(uiCounterTree())))
	ui := fixture.session.NativeUI()
	handles := make(map[domainnativeui.NodeID]uint64)
	for _, created := range applied.Created {
		handles[created.ID] = created.Handle
	}
	listenPayload := domainnativeui.EncodeListenRequest(domainnativeui.ListenRequest{
		Node:  domainnativeui.HandleFromNode(handles[3]),
		Event: domainnativeui.EventDrop,
	})
	listen, err := domainnativeui.DecodeListenResponse(
		dispatchUI(t, fixture, domainpluginabi.UiMethodListen, listenPayload))
	if err != nil {
		t.Fatalf("DecodeListenResponse: %v", err)
	}
	if listen.Status != domainnativeui.StatusOK {
		t.Fatalf("listen = %+v", listen)
	}

	// A raw guest id, a live handle, and a missing node are all normalized.
	for _, related := range []domainnativeui.NodeRef{
		domainnativeui.IDFromNode(2),
		domainnativeui.HandleFromNode(handles[2]),
		domainnativeui.IDFromNode(99),
	} {
		if !ui.EnqueueNode(3, domainnativeui.Event{Kind: domainnativeui.EventDrop, Related: related}) {
			t.Fatalf("drop with related %#x was not queued", uint64(related))
		}
	}
	events, err := domainnativeui.DecodePollResponse(
		dispatchUI(t, fixture, domainpluginabi.UiMethodPoll, domainnativeui.EncodePollRequest(0)))
	if err != nil {
		t.Fatalf("DecodePollResponse: %v", err)
	}
	if events.Status != domainnativeui.StatusOK || len(events.Events) != 3 {
		t.Fatalf("poll = %+v", events)
	}
	want := domainnativeui.HandleFromNode(handles[2])
	for i, event := range events.Events {
		if event.Kind != domainnativeui.EventDrop {
			t.Fatalf("event %d = %+v", i, event)
		}
		if i < 2 && event.Related != want {
			t.Fatalf("event %d related = %#x, want %#x", i, uint64(event.Related), uint64(want))
		}
		if i == 2 && event.Related != 0 {
			t.Fatalf("drop from a missing node related = %#x, want zero", uint64(event.Related))
		}
	}
}

func TestNativeUIFocusKeyboardAndEnvironment(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	fixture.grant(domainplugin.PermissionUINative, domainplugin.Scope{})
	applied := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodApply, domainnativeui.EncodeBatch(uiCounterTree())))
	ui := fixture.session.NativeUI()
	var inputHandle, buttonHandle uint64
	for _, created := range applied.Created {
		switch created.ID {
		case 3:
			inputHandle = created.Handle
		case 4:
			buttonHandle = created.Handle
		}
	}
	for _, registration := range []struct {
		node uint64
		kind domainnativeui.EventKind
	}{
		{inputHandle, domainnativeui.EventFocus},
		{inputHandle, domainnativeui.EventBlur},
		{inputHandle, domainnativeui.EventKeyDown},
		{buttonHandle, domainnativeui.EventFocus},
	} {
		payload := domainnativeui.EncodeListenRequest(domainnativeui.ListenRequest{
			Node: domainnativeui.HandleFromNode(registration.node), Event: registration.kind,
		})
		response, err := domainnativeui.DecodeListenResponse(
			dispatchUI(t, fixture, domainpluginabi.UiMethodListen, payload))
		if err != nil || response.Status != domainnativeui.StatusOK {
			t.Fatalf("listen = %+v, %v", response, err)
		}
	}

	if order := ui.FocusOrder(); len(order) != 2 || order[0] != 3 || order[1] != 4 {
		t.Fatalf("focus order = %v", order)
	}
	if !ui.SetFocus(3) {
		t.Fatal("SetFocus(input) failed")
	}
	if ui.Focus() != 3 {
		t.Fatalf("focus = %d", ui.Focus())
	}

	ui.SetEnvironment(appplugin.UIEnvironment{
		Locale: "en-US", Direction: appplugin.UIDirectionLTR,
		Shortcuts: []domainnativeui.Shortcut{{Mods: domainnativeui.ModMeta, Key: domainnativeui.KeyS, Command: "save"}},
	})
	if command, delivered := ui.Key(domainnativeui.ModMeta, domainnativeui.KeyS, true); command != "save" || delivered {
		t.Fatalf("host shortcut route = %q delivered=%v", command, delivered)
	}
	if command, delivered := ui.Key(0, domainnativeui.KeyA, true); command != "" || !delivered {
		t.Fatalf("plain key route = %q delivered=%v", command, delivered)
	}

	// A node that captures keys keeps them even for a host shortcut.
	if _, err := ui.Apply(domainnativeui.Batch{Ops: []domainnativeui.Op{
		{Kind: domainnativeui.OpSetProp, Node: domainnativeui.HandleFromNode(inputHandle),
			Prop: domainnativeui.PropKeyCapture, Value: domainnativeui.BoolValue(true)},
	}}); err != nil {
		t.Fatalf("set key capture: %v", err)
	}
	if command, delivered := ui.Key(domainnativeui.ModMeta, domainnativeui.KeyS, true); command != "" || !delivered {
		t.Fatalf("captured shortcut = %q delivered=%v", command, delivered)
	}

	// Focus traversal queues blur and focus for registered listeners.
	if !ui.SetFocus(4) {
		t.Fatal("SetFocus(button) failed")
	}
	events, err := domainnativeui.DecodePollResponse(
		dispatchUI(t, fixture, domainpluginabi.UiMethodPoll, domainnativeui.EncodePollRequest(0)))
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	kinds := make([]domainnativeui.EventKind, 0, len(events.Events))
	for _, event := range events.Events {
		kinds = append(kinds, event.Kind)
	}
	want := []domainnativeui.EventKind{
		domainnativeui.EventFocus,
		domainnativeui.EventKeyDown,
		domainnativeui.EventKeyDown,
		domainnativeui.EventBlur,
		domainnativeui.EventFocus,
	}
	if len(kinds) != len(want) {
		t.Fatalf("event kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event kinds = %v, want %v", kinds, want)
		}
	}
}

func TestNativeUIUnloadAndRevocationClearTheHandleTable(t *testing.T) {
	fixture := newServiceFixture(t, defaultServiceLimits())
	fixture.grant(domainplugin.PermissionUINative, domainplugin.Scope{})
	applied := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodApply, domainnativeui.EncodeBatch(uiCounterTree())))
	ui := fixture.session.NativeUI()
	inputHandle := uint64(0)
	for _, created := range applied.Created {
		if created.ID == 3 {
			inputHandle = created.Handle
		}
	}
	if _, ok := ui.NodeHandle(3); !ok {
		t.Fatal("node handle was not issued")
	}

	// Revoking the permission advances the epoch and closes the view.
	fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref), domainplugin.PermissionUINative)
	if !ui.Closed() {
		t.Fatal("revocation did not close the native ui session")
	}
	if _, ok := ui.NodeHandle(3); ok {
		t.Fatal("revoked view kept a node handle")
	}
	if got := decodeUIStatus(t, dispatchUI(t, fixture,
		domainpluginabi.UiMethodApply, domainnativeui.EncodeBatch(domainnativeui.Batch{Ops: []domainnativeui.Op{
			{Kind: domainnativeui.OpSetText, Node: domainnativeui.HandleFromNode(inputHandle), Text: "x"},
		}}))); got.Status != domainnativeui.StatusDenied {
		t.Fatalf("apply after revocation = %+v", got)
	}

	// A fresh session unloads cleanly as well.
	fresh := newServiceFixture(t, defaultServiceLimits())
	fresh.grant(domainplugin.PermissionUINative, domainplugin.Scope{})
	decodeUIStatus(t, dispatchUI(t, fresh,
		domainpluginabi.UiMethodApply, domainnativeui.EncodeBatch(uiCounterTree())))
	freshSession := fresh.session.NativeUI()
	fresh.host.Close(fresh.fiber)
	if !freshSession.Closed() {
		t.Fatal("host Close did not close the native ui session")
	}
	if freshSession.Tree() != nil {
		t.Fatal("closed session kept a tree")
	}
}
