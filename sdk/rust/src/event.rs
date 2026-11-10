//! Typed native UI event and request encoding.
//!
//! Events are queued by the host and only delivered through `ui.poll`; the
//! host never calls the guest synchronously. Frames are little-endian,
//! length-prefixed, and arrive in host queue order.

use crate::abi_v1::{EventKind, Key, UIStatus};

/// One native UI event. The node and related values are generation-tagged
/// handles; zero means no node or no related node.
#[derive(Clone, Debug, PartialEq)]
pub struct Event {
    /// The closed event kind.
    pub kind: EventKind,
    /// The node the event belongs to.
    pub node: u64,
    /// Host clock in Unix nanoseconds.
    pub time: i64,
    /// Keyboard key for key events.
    pub key: Key,
    /// Modifier bit mask.
    pub modifiers: u32,
    /// Mouse button index.
    pub button: i32,
    /// Item or row index.
    pub index: i32,
    /// Pointer X in local coordinates.
    pub x: f32,
    /// Pointer Y in local coordinates.
    pub y: f32,
    /// Pointer delta X.
    pub dx: f32,
    /// Pointer delta Y.
    pub dy: f32,
    /// Width for resize and item-request events.
    pub width: f32,
    /// Height for resize events.
    pub height: f32,
    /// The other node of a drag, drop, or resize, or a WebviewHandle.
    pub related: u64,
    /// UTF-8 event text, including the webview bridge JSON envelope.
    pub text: String,
}

/// One malformed event payload.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum EventError {
    /// The payload ended before a complete frame.
    Truncated,
    /// A string was not valid UTF-8.
    InvalidUtf8,
    /// The event kind is outside the closed set.
    UnknownKind(i32),
}

/// The fixed frame size excluding the text string.
pub const EVENT_FRAME_SIZE: usize = 76;

/// Decodes one poll payload. An empty payload is an empty event list.
pub fn decode_events(data: &[u8]) -> Result<Vec<Event>, EventError> {
    let mut cursor = Cursor::new(data);
    let mut events = Vec::new();
    while cursor.remaining() > 0 {
        events.push(cursor.event()?);
    }
    Ok(events)
}

/// Decodes one poll response: status u32, message string, event count, frames.
pub fn decode_poll_response(data: &[u8]) -> Result<PollResponse, EventError> {
    if data.len() < 8 {
        return Err(EventError::Truncated);
    }
    let mut cursor = Cursor::new(data);
    let status_code = cursor.u32() as i32;
    let status = UIStatus::from_code(status_code)
        .ok_or(EventError::UnknownKind(status_code))?;
    let message = cursor.string()?.to_string();
    let count = cursor.u32();
    let mut events = Vec::new();
    for _ in 0..count {
        events.push(cursor.event()?);
    }
    if cursor.remaining() != 0 {
        return Err(EventError::Truncated);
    }
    Ok(PollResponse { status, message, events })
}

/// One decoded poll response.
#[derive(Clone, Debug, PartialEq)]
pub struct PollResponse {
    /// The native UI status.
    pub status: UIStatus,
    /// The optional host diagnostic.
    pub message: String,
    /// Events in host queue order.
    pub events: Vec<Event>,
}

/// Encodes one listen request: node u64 and event kind u32.
pub fn encode_listen_request(node: u64, kind: EventKind) -> Vec<u8> {
    let mut out = Vec::with_capacity(12);
    out.extend_from_slice(&node.to_le_bytes());
    out.extend_from_slice(&(kind.code() as u32).to_le_bytes());
    out
}

/// Encodes one unlisten request: listener handle u64.
pub fn encode_unlisten_request(listener: u64) -> Vec<u8> {
    listener.to_le_bytes().to_vec()
}

/// Encodes one poll request: maximum events u32, or zero for everything.
pub fn encode_poll_request(max: u32) -> Vec<u8> {
    max.to_le_bytes().to_vec()
}

/// Encodes events for an explicit fixture. The guest normally only decodes;
/// this is the fake host and test half of the same wire format.
pub fn encode_events(events: &[Event]) -> Vec<u8> {
    let mut out = Vec::new();
    for event in events {
        out.extend_from_slice(&(event.kind.code() as u32).to_le_bytes());
        out.extend_from_slice(&0u32.to_le_bytes());
        out.extend_from_slice(&event.node.to_le_bytes());
        out.extend_from_slice(&(event.time as u64).to_le_bytes());
        out.extend_from_slice(&(event.key.code() as u32).to_le_bytes());
        out.extend_from_slice(&event.modifiers.to_le_bytes());
        out.extend_from_slice(&(event.button as u32).to_le_bytes());
        out.extend_from_slice(&(event.index as u32).to_le_bytes());
        out.extend_from_slice(&event.x.to_bits().to_le_bytes());
        out.extend_from_slice(&event.y.to_bits().to_le_bytes());
        out.extend_from_slice(&event.dx.to_bits().to_le_bytes());
        out.extend_from_slice(&event.dy.to_bits().to_le_bytes());
        out.extend_from_slice(&event.width.to_bits().to_le_bytes());
        out.extend_from_slice(&event.height.to_bits().to_le_bytes());
        out.extend_from_slice(&event.related.to_le_bytes());
        out.extend_from_slice(&(event.text.len() as u32).to_le_bytes());
        out.extend_from_slice(event.text.as_bytes());
    }
    out
}

struct Cursor<'a> {
    data: &'a [u8],
    position: usize,
}

impl<'a> Cursor<'a> {
    const fn new(data: &'a [u8]) -> Self {
        Self { data, position: 0 }
    }

    fn remaining(&self) -> usize { self.data.len().saturating_sub(self.position) }

    fn take(&mut self, count: usize) -> Result<&'a [u8], EventError> {
        if self.remaining() < count {
            return Err(EventError::Truncated);
        }
        let slice = &self.data[self.position..self.position + count];
        self.position += count;
        Ok(slice)
    }

    fn u32(&mut self) -> u32 {
        let bytes = self.take(4).unwrap_or(&[0, 0, 0, 0]);
        let mut raw = [0u8; 4];
        raw.copy_from_slice(bytes);
        u32::from_le_bytes(raw)
    }

    fn i64(&mut self) -> i64 {
        let bytes = self.take(8).unwrap_or(&[0; 8]);
        let mut raw = [0u8; 8];
        raw.copy_from_slice(bytes);
        i64::from_le_bytes(raw)
    }

    fn u64(&mut self) -> u64 {
        let bytes = self.take(8).unwrap_or(&[0; 8]);
        let mut raw = [0u8; 8];
        raw.copy_from_slice(bytes);
        u64::from_le_bytes(raw)
    }

    fn f32(&mut self) -> f32 {
        f32::from_bits(self.u32())
    }

    fn string(&mut self) -> Result<&'a str, EventError> {
        let length = self.u32() as usize;
        let bytes = self.take(length)?;
        core::str::from_utf8(bytes).map_err(|_| EventError::InvalidUtf8)
    }

    fn event(&mut self) -> Result<Event, EventError> {
        if self.remaining() < EVENT_FRAME_SIZE {
            return Err(EventError::Truncated);
        }
        let kind_code = self.u32() as i32;
        let kind = EventKind::from_code(kind_code).ok_or(EventError::UnknownKind(kind_code))?;
        let _reserved = self.u32();
        let node = self.u64();
        let time = self.i64();
        let key_code = self.u32() as i32;
        let key = Key::from_code(key_code).ok_or(EventError::UnknownKind(key_code))?;
        let modifiers = self.u32();
        let button = self.u32() as i32;
        let index = self.u32() as i32;
        let x = self.f32();
        let y = self.f32();
        let dx = self.f32();
        let dy = self.f32();
        let width = self.f32();
        let height = self.f32();
        let related = self.u64();
        let text = self.string()?.to_string();
        Ok(Event {
            kind, node, time, key, modifiers, button, index,
            x, y, dx, dy, width, height, related, text,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::abi_v1::EventKind;

    fn sample(kind: EventKind, text: &str) -> Event {
        Event {
            kind, node: 0x0000_0002_0000_0001, time: 77, key: Key::Enter,
            modifiers: 3, button: 1, index: 4,
            x: 1.5, y: 2.5, dx: 0.5, dy: -0.5, width: 100.0, height: 20.0,
            related: 9, text: text.to_string(),
        }
    }

    #[test]
    fn event_frames_round_trip_in_order() {
        let events = vec![sample(EventKind::Click, "hi"), sample(EventKind::Input, "typed")];
        let encoded = encode_events(&events);
        assert_eq!(decode_events(&encoded).unwrap(), events);
    }

    #[test]
    fn truncated_frames_are_rejected() {
        let encoded = encode_events(&[sample(EventKind::Click, "hi")]);
        assert_eq!(decode_events(&encoded[..10]), Err(EventError::Truncated));
    }
}
