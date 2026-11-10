//! In-language mock host for unit tests.
//!
//! The mock answers host service calls from the same shared stub definition
//! the CLI fake host uses (`sdk/idl/stub.json`, generated into `stub_gen`). It
//! also provides a virtual clock, deterministic randomness, provider stream
//! queues, and captured logs and output, so plugin logic can be tested without
//! building wasm or launching the app.

use std::collections::{HashMap, HashSet, VecDeque};
use std::sync::{Arc, Mutex, MutexGuard};

use crate::abi_v1::{self, host::Host, ServiceStatus};
use crate::stub_gen;

/// One mocked service call.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ServiceCall {
    /// The service name.
    pub service: String,
    /// The wire method id.
    pub method: u32,
    /// The raw request bytes.
    pub request: Vec<u8>,
    /// The transport status returned to the guest.
    pub status: i32,
    /// Bytes copied to the guest response buffer.
    pub response_len: u32,
}

/// One captured guest log line.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct LogEntry {
    /// The numeric generated log level.
    pub level: i32,
    /// The UTF-8 message.
    pub message: String,
}

/// One response delivered through a host response import.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ResponseRecord {
    /// The import that carried the response.
    pub kind: &'static str,
    /// The request id the guest echoed.
    pub request_id: u64,
    /// The payload bytes.
    pub payload: Vec<u8>,
}

/// One provider stream queue pair.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct MockStream {
    /// Bytes waiting for the provider to read.
    pub input: VecDeque<u8>,
    /// Bytes the provider wrote for the host to read.
    pub output: Vec<u8>,
    /// Output queue capacity in bytes.
    pub output_capacity: usize,
}

/// Mutable state shared by a mock host and its handle.
pub struct MockState {
    /// Captured guest log lines.
    pub logs: Vec<LogEntry>,
    /// Captured visible output.
    pub outputs: Vec<u8>,
    /// Every service call in order.
    pub calls: Vec<ServiceCall>,
    /// Virtual clock value used by the next now_ns call.
    pub now_ns: i64,
    /// Clock advance per now_ns call.
    pub clock_step_ns: i64,
    /// Deterministic random stream state.
    pub random_seed: u64,
    /// Service responses that override the shared stub definition.
    pub responses: HashMap<(String, u32), Vec<u8>>,
    /// In-flight request ids the mock reports as cancelled.
    pub cancelled: HashSet<u64>,
    /// Provider stream queues.
    pub streams: HashMap<u32, MockStream>,
    /// Responses delivered through language/service/provider/data imports.
    pub responses_sent: Vec<ResponseRecord>,
    /// MCP tool results.
    pub tool_results: Vec<(u32, Vec<u8>)>,
    /// MCP progress notifications.
    pub tool_progress: Vec<(u32, u32, String)>,
    /// Bytes copied by the last successful service call.
    pub last_response_len: u32,
    random_block: [u8; 8],
    random_used: usize,
}

impl MockState {
    /// Adds input bytes available to one provider stream.
    pub fn feed_stream(&mut self, stream: u32, bytes: &[u8]) {
        self.stream(stream).input.extend(bytes.iter().copied());
    }

    /// Returns the bytes a provider wrote to one stream.
    pub fn stream_output(&self, stream: u32) -> &[u8] {
        self.streams.get(&stream).map(|state| state.output.as_slice()).unwrap_or(&[])
    }

    /// Drains the output queue the way a host reader would.
    pub fn take_stream_output(&mut self, stream: u32) -> Vec<u8> {
        self.stream(stream).output.drain(..).collect()
    }

    /// Marks one in-flight request as cancelled.
    pub fn cancel_request(&mut self, request_id: u64) {
        self.cancelled.insert(request_id);
    }

    fn stream(&mut self, id: u32) -> &mut MockStream {
        let state = self.streams.entry(id).or_insert_with(|| MockStream {
            input: VecDeque::new(),
            output: Vec::new(),
            output_capacity: 4096,
        });
        if state.output_capacity == 0 {
            state.output_capacity = 4096;
        }
        state
    }

    fn next_random_byte(&mut self) -> u8 {
        if self.random_used == self.random_block.len() {
            self.random_seed = self.random_seed.wrapping_add(0x9E37_79B9_7F4A_7C15);
            let mut value = self.random_seed;
            value = (value ^ (value >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
            value = (value ^ (value >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
            value ^= value >> 31;
            self.random_block = value.to_le_bytes();
            self.random_used = 0;
        }
        let byte = self.random_block[self.random_used];
        self.random_used += 1;
        byte
    }
}

/// A clonable handle to an installed mock host's state.
#[derive(Clone)]
pub struct MockHandle {
    state: Arc<Mutex<MockState>>,
}

impl MockHandle {
    /// Runs a closure with the mock state.
    pub fn with<R>(&self, f: impl FnOnce(&mut MockState) -> R) -> R {
        let mut state = lock(&self.state);
        f(&mut state)
    }

    /// Removes the installed host. Further generated calls fall back to the
    /// no-host defaults.
    pub fn uninstall(self) {
        let _ = abi_v1::host::uninstall();
    }
}

/// The installed mock host. Build it, configure fixtures with the handle, and
/// keep the handle to inspect the results.
pub struct MockHost {
    state: Arc<Mutex<MockState>>,
}

impl MockHost {
    /// Creates a mock with the shared stub clock, seed, and responses.
    pub fn new() -> Self {
        Self {
            state: Arc::new(Mutex::new(MockState {
                logs: Vec::new(),
                outputs: Vec::new(),
                calls: Vec::new(),
                now_ns: stub_gen::CLOCK_START_NS,
                clock_step_ns: stub_gen::CLOCK_STEP_NS,
                random_seed: stub_gen::RANDOM_SEED,
                responses: HashMap::new(),
                cancelled: HashSet::new(),
                streams: HashMap::new(),
                responses_sent: Vec::new(),
                tool_results: Vec::new(),
                tool_progress: Vec::new(),
                last_response_len: 0,
                random_block: [0; 8],
                random_used: 8,
            })),
        }
    }

    /// Returns a handle to the same state.
    pub fn handle(&self) -> MockHandle {
        MockHandle { state: Arc::clone(&self.state) }
    }

    /// Sets the virtual clock.
    pub fn with_clock(self, start_ns: i64, step_ns: i64) -> Self {
        lock(&self.state).now_ns = start_ns;
        lock(&self.state).clock_step_ns = step_ns;
        self
    }

    /// Sets the deterministic random seed.
    pub fn with_seed(self, seed: u64) -> Self {
        lock(&self.state).random_seed = seed;
        self
    }

    /// Installs the mock for the generated native bindings and returns its
    /// handle.
    pub fn install(self) -> MockHandle {
        let handle = self.handle();
        let _ = abi_v1::host::install(Box::new(self));
        handle
    }
}

impl Default for MockHost {
    fn default() -> Self { Self::new() }
}

fn lock(state: &Arc<Mutex<MockState>>) -> MutexGuard<'_, MockState> {
    state.lock().unwrap_or_else(|poisoned| poisoned.into_inner())
}

impl Host for MockHost {
    fn abi_version(&mut self) -> i32 { abi_v1::ABI_VERSION }

    fn log(&mut self, level: i32, message: &[u8]) {
        lock(&self.state).logs.push(LogEntry {
            level,
            message: String::from_utf8_lossy(message).into_owned(),
        });
    }

    fn now_ns(&mut self) -> i64 {
        let mut state = lock(&self.state);
        let value = state.now_ns;
        state.now_ns = state.now_ns.saturating_add(state.clock_step_ns);
        value
    }

    fn random_get(&mut self, buffer: &mut [u8]) -> i32 {
        let mut state = lock(&self.state);
        for byte in buffer {
            *byte = state.next_random_byte();
        }
        0
    }

    fn output_write(&mut self, data: &[u8]) -> i32 {
        lock(&self.state).outputs.extend_from_slice(data);
        data.len() as i32
    }

    fn service_call(&mut self, service: &str, method_id: u32, request: &[u8],
                    response: &mut [u8]) -> i32 {
        let mut state = lock(&self.state);
        state.last_response_len = 0;
        let payload = state
            .responses
            .get(&(service.to_string(), method_id))
            .cloned()
            .or_else(|| {
                stub_gen::service_response(service, method_id).map(|payload| payload.to_vec())
            })
            // An undefined stub is a service-level not-found envelope, the
            // same value the Go fake host returns. The transport remains OK.
            .unwrap_or_else(|| stub_gen::absent_response(service, method_id));
        let status = if payload.len() > response.len() {
            ServiceStatus::Limit.code()
        } else {
            response[..payload.len()].copy_from_slice(&payload);
            state.last_response_len = payload.len() as u32;
            ServiceStatus::Ok.code()
        };
        let response_len = state.last_response_len;
        state.calls.push(ServiceCall {
            service: service.to_string(),
            method: method_id,
            request: request.to_vec(),
            status,
            response_len,
        });
        status
    }

    fn service_response_len(&mut self) -> u32 {
        lock(&self.state).last_response_len
    }

    fn language_respond(&mut self, request_id: u64, payload: &[u8]) -> i32 {
        record_response(&self.state, "language", request_id, payload)
    }

    fn language_cancelled(&mut self, request_id: u64) -> i32 {
        i32::from(lock(&self.state).cancelled.contains(&request_id))
    }

    fn mcp_tool_result(&mut self, request_id: u32, payload: &[u8]) -> i32 {
        lock(&self.state).tool_results.push((request_id, payload.to_vec()));
        0
    }

    fn mcp_tool_progress(&mut self, request_id: u32, progress: u32, message: &[u8]) -> i32 {
        lock(&self.state).tool_progress.push((
            request_id,
            progress,
            String::from_utf8_lossy(message).into_owned(),
        ));
        0
    }

    fn service_respond(&mut self, request_id: u64, payload: &[u8]) -> i32 {
        record_response(&self.state, "service", request_id, payload)
    }

    fn provider_respond(&mut self, request_id: u64, payload: &[u8]) -> i32 {
        record_response(&self.state, "provider", request_id, payload)
    }

    fn provider_cancelled(&mut self, request_id: u64) -> i32 {
        i32::from(lock(&self.state).cancelled.contains(&request_id))
    }

    fn provider_input_read(&mut self, stream: u32, buffer: &mut [u8]) -> i32 {
        let mut state = lock(&self.state);
        let state = state.stream(stream);
        let count = buffer.len().min(state.input.len());
        for (index, byte) in state.input.drain(..count).enumerate() {
            buffer[index] = byte;
        }
        count as i32
    }

    fn provider_input_credit(&mut self, stream: u32) -> i32 {
        let mut state = lock(&self.state);
        state.stream(stream).input.len() as i32
    }

    fn provider_output_write(&mut self, stream: u32, data: &[u8]) -> i32 {
        let mut state = lock(&self.state);
        let state = state.stream(stream);
        if state.output.len() + data.len() > state.output_capacity {
            return -1;
        }
        state.output.extend_from_slice(data);
        0
    }

    fn provider_output_credit(&mut self, stream: u32) -> i32 {
        let mut state = lock(&self.state);
        let state = state.stream(stream);
        state.output_capacity.saturating_sub(state.output.len()) as i32
    }

    fn data_provider_respond(&mut self, request_id: u64, payload: &[u8]) -> i32 {
        record_response(&self.state, "data", request_id, payload)
    }

    fn data_provider_cancelled(&mut self, request_id: u64) -> i32 {
        i32::from(lock(&self.state).cancelled.contains(&request_id))
    }
}

fn record_response(state: &Arc<Mutex<MockState>>, kind: &'static str,
                   request_id: u64, payload: &[u8]) -> i32 {
    lock(state).responses_sent.push(ResponseRecord {
        kind,
        request_id,
        payload: payload.to_vec(),
    });
    0
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::abi_v1::service::storage;
    use crate::service::ServiceCode;

    #[test]
    fn typed_service_calls_use_the_shared_stub() {
        let _serial = crate::mock::TEST_LOCK.lock().unwrap_or_else(|p| p.into_inner());
        let host = MockHost::new();
        let handle = host.install();
        let mut response = [0u8; 512];
        let reply = storage::get(b"{}", &mut response);
        assert!(reply.is_ok());
        let envelope = reply.envelope().unwrap();
        assert_eq!(envelope.code, ServiceCode::Ok);
        let calls = handle.with(|state| state.calls.clone());
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0].service, "storage");
        assert_eq!(calls[0].method, storage::METHOD_GET);
        assert!(calls[0].response_len > 0);
        handle.uninstall();
    }

    #[test]
    fn provider_stream_pump_round_trips_input() {
        let _serial = crate::mock::TEST_LOCK.lock().unwrap_or_else(|p| p.into_inner());
        let host = MockHost::new();
        let handle = host.install();
        handle.with(|state| state.feed_stream(4, b"hello"));
        let stream = crate::stream::ProviderStream::new(4);
        let result = stream.pump(64, 8).unwrap();
        assert_eq!(result.moved, 5);
        assert!(result.input_exhausted);
        assert_eq!(handle.with(|state| state.stream_output(4).to_vec()), b"hello");
        handle.uninstall();
    }
}

/// Test serialization for the process-wide installed host.
#[cfg(test)]
pub(crate) static TEST_LOCK: Mutex<()> = Mutex::new(());
