//! Behavioural tests for the reference SDK's typed surface, run on the native
//! target against the in-language mock host.

use std::sync::Mutex;

use natcatty_plugin_sdk::abi_v1::{self, service};
use natcatty_plugin_sdk::event::{self, Event};
use natcatty_plugin_sdk::mock::{MockHandle, MockHost, MockState};
use natcatty_plugin_sdk::service::ServiceCode;
use natcatty_plugin_sdk::stub_gen;
use natcatty_plugin_sdk::stream::ProviderStream;
use natcatty_plugin_sdk::ui::{self, BatchBuilder, Value};
use natcatty_plugin_sdk::{NodeKind, PropID, UIStatus};

/// The generated mock installs one process-wide host, so tests take turns.
static TEST_LOCK: Mutex<()> = Mutex::new(());

fn install() -> (MockHandle, std::sync::MutexGuard<'static, ()>) {
    let guard = TEST_LOCK.lock().unwrap_or_else(|poisoned| poisoned.into_inner());
    let host = MockHost::new();
    (host.install(), guard)
}

fn push_u32(out: &mut Vec<u8>, value: u32) {
    out.extend_from_slice(&value.to_le_bytes());
}

fn push_u64(out: &mut Vec<u8>, value: u64) {
    out.extend_from_slice(&value.to_le_bytes());
}

fn push_string(out: &mut Vec<u8>, value: &str) {
    push_u32(out, value.len() as u32);
    out.extend_from_slice(value.as_bytes());
}

fn status_response(revision: u64, created: &[(u32, u64)]) -> Vec<u8> {
    let mut out = Vec::new();
    push_u32(&mut out, UIStatus::Ok.code() as u32);
    push_u64(&mut out, revision);
    push_string(&mut out, "");
    push_u32(&mut out, created.len() as u32);
    for (id, handle) in created {
        push_u32(&mut out, *id);
        push_u64(&mut out, *handle);
    }
    out
}

#[test]
fn generated_typed_methods_use_the_shared_stub() {
    let (handle, _guard) = install();
    let mut response = [0u8; 512];

    let reply = service::storage::get(b"{\"key\":\"x\"}", &mut response);
    assert!(reply.is_ok());
    let envelope = reply.envelope().unwrap();
    assert_eq!(envelope.code, ServiceCode::Ok);
    assert_eq!(reply.payload.len(), handle.with(|state| state.calls[0].response_len as usize));

    let mut response = [0u8; 512];
    let reply = service::auth::begin(b"{\"host_id\":\"host-1\",\"method\":\"none\"}", &mut response);
    assert!(reply.is_ok());
    assert_eq!(reply.envelope().unwrap().code, ServiceCode::Ok);

    let mut response = [0u8; 4096];
    let reply = service::services::invoke(b"{}", &mut response);
    assert!(reply.is_ok());
    assert_eq!(reply.envelope().unwrap().code, ServiceCode::Ok);

    let calls = handle.with(|state| state.calls.clone());
    assert_eq!(calls.len(), 3);
    assert_eq!(calls[0].method, service::storage::METHOD_GET);
    assert_eq!(calls[1].method, service::auth::METHOD_BEGIN);
    assert_eq!(calls[2].method, service::services::METHOD_INVOKE);
    handle.uninstall();
}

#[test]
fn every_stubbed_call_has_a_payload() {
    let stubbed: Vec<_> = stub_gen::stubbed_calls().collect();
    assert!(stubbed.len() >= 50);
    for (service, method) in stubbed {
        assert!(
            stub_gen::service_response(service, method).is_some(),
            "missing payload for {service}.{method}"
        );
    }
    assert_eq!(
        stub_gen::service_response("storage", service::storage::METHOD_GET).unwrap(),
        br#"{"code":"ok","data":{"value":""}}"#
    );
}

#[test]
fn missing_stub_matches_the_fake_host_not_found_envelope() {
    let (handle, _guard) = install();
    let mut response = [0u8; 128];
    let reply = service::storage::call(999, b"{}", &mut response);
    assert!(reply.is_ok());
    assert_eq!(reply.envelope().unwrap().code, ServiceCode::NotFound);
    assert_eq!(
        reply.payload,
        br#"{"code":"not-found","message":"unknown method 999 for service \"storage\""}"#
    );

    // A declared method with no entry in stub.json is still a service-level
    // not-found, not a transport failure.
    let mut response = [0u8; 128];
    let reply = service::files::read(b"{}", &mut response);
    assert!(reply.is_ok());
    assert_eq!(reply.envelope().unwrap().code, ServiceCode::NotFound);
    assert_eq!(
        reply.payload,
        br#"{"code":"not-found","message":"no stub response for files.read"}"#
    );
    let calls = handle.with(|state| state.calls.clone());
    assert_eq!(calls[0].status, abi_v1::ServiceStatus::Ok.code());
    assert_eq!(calls[1].status, abi_v1::ServiceStatus::Ok.code());
    handle.uninstall();
}

#[test]
fn clock_and_randomness_are_reproducible() {
    let (_handle, _guard) = install();
    let first_clock = [abi_v1::now_ns(), abi_v1::now_ns()];
    let mut first_random = [0u8; 16];
    assert_eq!(abi_v1::random_get(&mut first_random), 0);
    abi_v1::host::uninstall();

    let _second = MockHost::new().install();
    let second_clock = [abi_v1::now_ns(), abi_v1::now_ns()];
    let mut second_random = [0u8; 16];
    assert_eq!(abi_v1::random_get(&mut second_random), 0);
    abi_v1::host::uninstall();

    assert_eq!(first_clock, second_clock);
    assert_eq!(first_random, second_random);
    assert_eq!(first_clock[0], stub_gen::CLOCK_START_NS);
    assert_eq!(first_clock[1], stub_gen::CLOCK_START_NS + stub_gen::CLOCK_STEP_NS);
}

#[test]
fn ui_apply_encodes_a_tree_and_decodes_generated_handles() {
    let (handle, _guard) = install();
    handle.with(|state| {
        state.responses.insert(
            ("ui".to_string(), service::ui::METHOD_APPLY),
            status_response(11, &[(1, 0x0000_0001_0000_0001), (2, 0x0000_0001_0000_0002)]),
        );
    });

    let mut batch = BatchBuilder::new(1);
    batch.create(1, NodeKind::Container);
    batch.set_prop(1, PropID::Layout, Value::Int(1));
    batch.set_children(1, &[2]);
    batch.create(2, NodeKind::Text);
    batch.set_text(2, "Count: 0");

    let result = ui::apply(&batch).unwrap();
    assert_eq!(result.revision, 11);
    assert_eq!(result.created.len(), 2);
    assert_eq!(result.created[1].0, 2);
    let calls = handle.with(|state| state.calls.clone());
    assert_eq!(calls[0].service, "ui");
    assert_eq!(calls[0].method, service::ui::METHOD_APPLY);
    assert!(calls[0].request.len() > 40);
    handle.uninstall();
}

#[test]
fn ui_listen_and_poll_decode_events_in_queue_order() {
    let (handle, _guard) = install();
    handle.with(|state| {
        let mut listen = Vec::new();
        push_u32(&mut listen, UIStatus::Ok.code() as u32);
        push_u64(&mut listen, 0x0000_0002_0000_0007);
        push_string(&mut listen, "");
        state.responses.insert(("ui".to_string(), service::ui::METHOD_LISTEN), listen);

        let events = vec![
            Event {
                kind: abi_v1::EventKind::Click, node: 0x0000_0001_0000_0002, time: 1,
                key: abi_v1::Key::Unknown, modifiers: 0, button: 0, index: 0,
                x: 0.0, y: 0.0, dx: 0.0, dy: 0.0, width: 0.0, height: 0.0,
                related: 0, text: "first".to_string(),
            },
            Event {
                kind: abi_v1::EventKind::Input, node: 0x0000_0001_0000_0003, time: 2,
                key: abi_v1::Key::Enter, modifiers: 0, button: 0, index: 0,
                x: 0.0, y: 0.0, dx: 0.0, dy: 0.0, width: 0.0, height: 0.0,
                related: 0, text: "second".to_string(),
            },
        ];
        let mut poll = Vec::new();
        push_u32(&mut poll, UIStatus::Ok.code() as u32);
        push_string(&mut poll, "");
        push_u32(&mut poll, events.len() as u32);
        poll.extend_from_slice(&event::encode_events(&events));
        state.responses.insert(("ui".to_string(), service::ui::METHOD_POLL), poll);
    });

    let listener = ui::listen(0x0000_0001_0000_0002, abi_v1::EventKind::Click).unwrap();
    assert_eq!(listener.handle, 0x0000_0002_0000_0007);
    let events = ui::poll(0).unwrap();
    assert_eq!(events.len(), 2);
    assert_eq!(events[0].text, "first");
    assert_eq!(events[1].text, "second");
    assert_eq!(events[1].kind, abi_v1::EventKind::Input);
    handle.uninstall();
}

#[test]
fn provider_stream_pump_respects_output_credit() {
    let (handle, _guard) = install();
    handle.with(|state| {
        state.feed_stream(9, b"abcdefghij");
        state.streams.get_mut(&9).unwrap().output_capacity = 4;
    });
    let stream = ProviderStream::new(9);
    let first = stream.pump(16, 8).unwrap();
    assert_eq!(first.moved, 4);
    assert!(first.output_blocked);
    let drained = handle.with(|state| state.take_stream_output(9));
    assert_eq!(drained, b"abcd");
    let second = stream.pump(16, 8).unwrap();
    assert_eq!(second.moved, 4);
    let drained = handle.with(|state| state.take_stream_output(9));
    assert_eq!(drained, b"efgh");
    handle.uninstall();
}

#[test]
fn service_override_replaces_the_shared_stub() {
    let (handle, _guard) = install();
    handle.with(|state| {
        state.responses.insert(
            ("storage".to_string(), service::storage::METHOD_GET),
            br#"{"code":"ok","data":{"value":"override"}}"#.to_vec(),
        );
    });
    let mut response = [0u8; 256];
    let reply = service::storage::get(b"{}", &mut response);
    assert_eq!(
        reply.envelope().unwrap().data_string("value"),
        Some("override")
    );
    handle.uninstall();
}

#[test]
fn mock_state_is_observable_after_plugin_code() {
    let (handle, _guard) = install();
    abi_v1::log(abi_v1::LogLevel::Info, b"from plugin");
    abi_v1::output_write(b"visible");
    handle.with(|state: &mut MockState| {
        assert_eq!(state.logs.len(), 1);
        assert_eq!(state.logs[0].message, "from plugin");
        assert_eq!(state.outputs, b"visible");
    });
    handle.uninstall();
}
