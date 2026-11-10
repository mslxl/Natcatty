//! Typed native UI nodes and calls.
//!
//! A batch is one atomic retained-tree patch. Guest node ids are u32 inside a
//! batch; the host returns generation-tagged `NodeHandle`s after a successful
//! apply. The host never calls the guest: events arrive only through poll.

use crate::abi_v1::{self, EventKind, NodeKind, PropID, PropValueKind, UIStatus, UIOpKind};
use crate::event::{self, Event};

/// Maximum encoded apply response the convenience calls accept. A batch that
/// creates more handles than this should use the buffer-taking variant.
pub const DEFAULT_RESPONSE_BYTES: usize = 256 * 1024;

/// One property value. Bool values carry 0 or 1 in the integer slot, matching
/// the host wire format.
#[derive(Clone, Debug, PartialEq)]
pub enum Value {
    /// A boolean property.
    Bool(bool),
    /// A signed 64-bit integer property.
    Int(i64),
    /// A 64-bit floating-point property.
    Float(f64),
    /// A UTF-8 string property.
    Str(String),
    /// A node reference property.
    Ref(u64),
}

impl Value {
    const fn kind(&self) -> PropValueKind {
        match self {
            Self::Bool(_) => PropValueKind::Bool,
            Self::Int(_) => PropValueKind::Int,
            Self::Float(_) => PropValueKind::Float,
            Self::Str(_) => PropValueKind::String,
            Self::Ref(_) => PropValueKind::Ref,
        }
    }
}

/// One native UI apply result.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ApplyResult {
    /// The committed tree revision.
    pub revision: u64,
    /// The handles issued for guest node ids.
    pub created: Vec<(u32, u64)>,
}

/// One decoded listener response.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Listener {
    /// The new generation-tagged listener handle.
    pub handle: u64,
}

/// One native UI call failure.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum UiError {
    /// The host returned a status other than ok.
    Status(UIStatus, String),
    /// The host returned a transport failure.
    Transport(i32),
    /// The response payload was malformed.
    Malformed,
    /// The UI transport status was outside the closed vocabulary.
    UnknownStatus(u32),
}

/// Encodes one atomic patch batch.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct BatchBuilder {
    root: u64,
    count: u32,
    ops: Vec<u8>,
}

impl BatchBuilder {
    /// Starts a batch that selects `root` after applying every op.
    pub fn new(root: u64) -> Self {
        Self { root, count: 0, ops: Vec::new() }
    }

    /// Appends a create op for one guest node id.
    pub fn create(&mut self, id: u32, kind: NodeKind) -> &mut Self {
        self.begin(UIOpKind::Create);
        push_u32(&mut self.ops, id);
        push_u32(&mut self.ops, kind.code() as u32);
        self
    }

    /// Appends a destroy op for one node reference.
    pub fn destroy(&mut self, node: u64) -> &mut Self {
        self.begin(UIOpKind::Destroy);
        push_u64(&mut self.ops, node);
        self
    }

    /// Appends an insert op.
    pub fn insert(&mut self, parent: u64, index: u32, child: u64) -> &mut Self {
        self.begin(UIOpKind::Insert);
        push_u64(&mut self.ops, parent);
        push_u32(&mut self.ops, index);
        push_u64(&mut self.ops, child);
        self
    }

    /// Appends a move op.
    pub fn move_node(&mut self, node: u64, parent: u64, index: u32) -> &mut Self {
        self.begin(UIOpKind::Move);
        push_u64(&mut self.ops, node);
        push_u64(&mut self.ops, parent);
        push_u32(&mut self.ops, index);
        self
    }

    /// Appends a set-property op.
    pub fn set_prop(&mut self, node: u64, prop: PropID, value: Value) -> &mut Self {
        self.begin(UIOpKind::SetProp);
        push_u64(&mut self.ops, node);
        push_u32(&mut self.ops, prop.code() as u32);
        push_u32(&mut self.ops, value.kind().code() as u32);
        match value {
            Value::Bool(value) => {
                push_u64(&mut self.ops, u64::from(value));
                push_u64(&mut self.ops, 0);
                push_u64(&mut self.ops, 0);
                push_string(&mut self.ops, "");
            }
            Value::Int(value) => {
                push_u64(&mut self.ops, value as u64);
                push_u64(&mut self.ops, 0);
                push_u64(&mut self.ops, 0);
                push_string(&mut self.ops, "");
            }
            Value::Float(value) => {
                push_u64(&mut self.ops, 0);
                push_u64(&mut self.ops, value.to_bits());
                push_u64(&mut self.ops, 0);
                push_string(&mut self.ops, "");
            }
            Value::Str(value) => {
                push_u64(&mut self.ops, 0);
                push_u64(&mut self.ops, 0);
                push_u64(&mut self.ops, 0);
                push_string(&mut self.ops, &value);
            }
            Value::Ref(value) => {
                push_u64(&mut self.ops, 0);
                push_u64(&mut self.ops, 0);
                push_u64(&mut self.ops, value);
                push_string(&mut self.ops, "");
            }
        }
        self
    }

    /// Appends a set-text op.
    pub fn set_text(&mut self, node: u64, text: &str) -> &mut Self {
        self.begin(UIOpKind::SetText);
        push_u64(&mut self.ops, node);
        push_string(&mut self.ops, text);
        self
    }

    /// Appends a set-children op.
    pub fn set_children(&mut self, node: u64, children: &[u64]) -> &mut Self {
        self.begin(UIOpKind::SetChildren);
        push_u64(&mut self.ops, node);
        push_u32(&mut self.ops, children.len() as u32);
        for child in children {
            push_u64(&mut self.ops, *child);
        }
        self
    }

    /// Encodes the root, op count, and ops.
    pub fn encode(&self) -> Vec<u8> {
        let mut out = Vec::with_capacity(12 + self.ops.len());
        push_u64(&mut out, self.root);
        push_u32(&mut out, self.count);
        out.extend_from_slice(&self.ops);
        out
    }

    fn begin(&mut self, kind: UIOpKind) {
        self.ops.push(kind.code() as u8);
        self.count += 1;
    }
}

/// A small typed node builder. Appending it emits create, properties, text,
/// and children in one deterministic order.
#[derive(Clone, Debug, PartialEq)]
pub struct NodeBuilder {
    /// The guest node id.
    pub id: u32,
    /// The node kind.
    pub kind: NodeKind,
    /// The node text, when set.
    pub text: Option<String>,
    /// Typed properties.
    pub props: Vec<(PropID, Value)>,
    /// Guest ids of direct children.
    pub children: Vec<u32>,
}

impl NodeBuilder {
    /// Starts one node.
    pub fn new(id: u32, kind: NodeKind) -> Self {
        Self { id, kind, text: None, props: Vec::new(), children: Vec::new() }
    }

    /// Sets the node text.
    pub fn text(mut self, text: impl Into<String>) -> Self {
        self.text = Some(text.into());
        self
    }

    /// Adds one property.
    pub fn prop(mut self, prop: PropID, value: Value) -> Self {
        self.props.push((prop, value));
        self
    }

    /// Adds one child guest id.
    pub fn child(mut self, id: u32) -> Self {
        self.children.push(id);
        self
    }

    /// Appends this node to a batch.
    pub fn append_to(self, batch: &mut BatchBuilder) -> &mut BatchBuilder {
        let node = u64::from(self.id);
        batch.create(self.id, self.kind);
        for (prop, value) in self.props {
            batch.set_prop(node, prop, value);
        }
        if let Some(text) = self.text {
            batch.set_text(node, &text);
        }
        if !self.children.is_empty() {
            let children: Vec<u64> = self.children.iter().map(|id| u64::from(*id)).collect();
            batch.set_children(node, &children);
        }
        batch
    }
}

/// Commits one batch through `ui.apply`.
pub fn apply(batch: &BatchBuilder) -> Result<ApplyResult, UiError> {
    apply_with_buffer(batch, DEFAULT_RESPONSE_BYTES)
}

/// Commits one batch with an explicit response buffer size.
pub fn apply_with_buffer(batch: &BatchBuilder, capacity: usize) -> Result<ApplyResult, UiError> {
    let request = batch.encode();
    let mut response = vec![0u8; capacity];
    let reply = abi_v1::service::ui::apply(&request, &mut response);
    let payload = check_status(reply.status, reply.payload)?;
    decode_status_response(payload)
}

/// Registers one listener and returns its handle.
pub fn listen(node: u64, kind: EventKind) -> Result<Listener, UiError> {
    let request = event::encode_listen_request(node, kind);
    let mut response = vec![0u8; 256];
    let reply = abi_v1::service::ui::listen(&request, &mut response);
    let payload = check_status(reply.status, reply.payload)?;
    let mut cursor = Reader::new(payload);
    let status = decode_status(cursor.u32())?;
    let handle = cursor.u64();
    let message = cursor.string();
    if status != UIStatus::Ok {
        return Err(UiError::Status(status, message));
    }
    Ok(Listener { handle })
}

/// Releases one listener handle.
pub fn unlisten(listener: u64) -> Result<(), UiError> {
    let request = event::encode_unlisten_request(listener);
    let mut response = vec![0u8; 64];
    let reply = abi_v1::service::ui::unlisten(&request, &mut response);
    let payload = check_status(reply.status, reply.payload)?;
    decode_status_only(payload)
}

/// Drains queued events. Zero asks for everything the queue holds.
pub fn poll(max: u32) -> Result<Vec<Event>, UiError> {
    let request = event::encode_poll_request(max);
    let mut response = vec![0u8; DEFAULT_RESPONSE_BYTES];
    let reply = abi_v1::service::ui::poll(&request, &mut response);
    let payload = check_status(reply.status, reply.payload)?;
    match event::decode_poll_response(payload) {
        Ok(parsed) => {
            if parsed.status != UIStatus::Ok {
                return Err(UiError::Status(parsed.status, parsed.message));
            }
            Ok(parsed.events)
        }
        Err(_) => Err(UiError::Malformed),
    }
}

/// Answers one webview bridge request.
pub fn webview_respond(handle: u64, request_id: u64, status: u32, payload: &str) -> Result<(), UiError> {
    let mut body = Vec::with_capacity(28 + payload.len());
    push_u64(&mut body, handle);
    push_u64(&mut body, request_id);
    push_u32(&mut body, status);
    push_string(&mut body, payload);
    let mut response = vec![0u8; 64];
    let reply = abi_v1::service::ui::webview_respond(&body, &mut response);
    let response_payload = check_status(reply.status, reply.payload)?;
    decode_status_only(response_payload)
}

fn check_status(status: i32, payload: &[u8]) -> Result<&[u8], UiError> {
    if status != 0 {
        return Err(UiError::Transport(status));
    }
    if payload.is_empty() {
        return Err(UiError::Malformed);
    }
    Ok(payload)
}

fn decode_status(code: u32) -> Result<UIStatus, UiError> {
    UIStatus::from_code(code as i32).ok_or(UiError::UnknownStatus(code))
}

fn decode_status_only(payload: &[u8]) -> Result<(), UiError> {
    let mut cursor = Reader::new(payload);
    let status = decode_status(cursor.u32())?;
    let _revision = cursor.u64();
    let message = cursor.string();
    let _created = cursor.u32();
    if status != UIStatus::Ok {
        return Err(UiError::Status(status, message));
    }
    Ok(())
}

fn decode_status_response(payload: &[u8]) -> Result<ApplyResult, UiError> {
    let mut cursor = Reader::new(payload);
    let status = decode_status(cursor.u32())?;
    let revision = cursor.u64();
    let message = cursor.string();
    let count = cursor.u32();
    let mut created = Vec::with_capacity(count as usize);
    for _ in 0..count {
        created.push((cursor.u32(), cursor.u64()));
    }
    if status != UIStatus::Ok {
        return Err(UiError::Status(status, message));
    }
    Ok(ApplyResult { revision, created })
}

struct Reader<'a> {
    data: &'a [u8],
    position: usize,
}

impl<'a> Reader<'a> {
    const fn new(data: &'a [u8]) -> Self { Self { data, position: 0 } }

    fn take(&mut self, count: usize) -> &'a [u8] {
        if self.position + count > self.data.len() {
            self.position = self.data.len();
            return &[];
        }
        let slice = &self.data[self.position..self.position + count];
        self.position += count;
        slice
    }

    fn u32(&mut self) -> u32 {
        let bytes = self.take(4);
        if bytes.len() != 4 { return 0; }
        u32::from_le_bytes([bytes[0], bytes[1], bytes[2], bytes[3]])
    }

    fn u64(&mut self) -> u64 {
        let bytes = self.take(8);
        if bytes.len() != 8 { return 0; }
        u64::from_le_bytes([
            bytes[0], bytes[1], bytes[2], bytes[3],
            bytes[4], bytes[5], bytes[6], bytes[7],
        ])
    }

    fn string(&mut self) -> String {
        let length = self.u32() as usize;
        let bytes = self.take(length);
        String::from_utf8_lossy(bytes).into_owned()
    }
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn node_builder_encodes_the_host_wire_layout() {
        let mut batch = BatchBuilder::new(1);
        NodeBuilder::new(1, NodeKind::Container)
            .prop(PropID::Layout, Value::Int(1))
            .child(2)
            .append_to(&mut batch);
        NodeBuilder::new(2, NodeKind::Text).text("Count: 0").append_to(&mut batch);
        let encoded = batch.encode();
        assert_eq!(u64::from_le_bytes(encoded[0..8].try_into().unwrap()), 1);
        assert_eq!(u32::from_le_bytes(encoded[8..12].try_into().unwrap()), 5);
        assert_eq!(encoded[12], UIOpKind::Create.code() as u8);
        assert_eq!(encoded[13..17], 1u32.to_le_bytes());
        assert_eq!(encoded[17..21], (NodeKind::Container.code() as u32).to_le_bytes());
        assert!(encoded.len() > 60);
    }

    #[test]
    fn status_response_decodes_created_handles() {
        let mut payload = Vec::new();
        push_u32(&mut payload, UIStatus::Ok.code() as u32);
        push_u64(&mut payload, 7);
        push_string(&mut payload, "");
        push_u32(&mut payload, 1);
        push_u32(&mut payload, 3);
        push_u64(&mut payload, 0x0000_0002_0000_0003);
        let result = decode_status_response(&payload).unwrap();
        assert_eq!(result.revision, 7);
        assert_eq!(result.created, vec![(3, 0x0000_0002_0000_0003)]);
    }
}
