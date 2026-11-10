//! Typed helpers over the generated host-service bindings.
//!
//! Every service response is a JSON envelope:
//!
//! ```json
//! {"code":"ok|denied|not-found|conflict|invalid|limit|fault","message":"...","data":{...}}
//! ```
//!
//! The transport status says whether the call crossed the boundary; the
//! envelope code says whether the host allowed it. This module reads the
//! envelope without a JSON dependency so the SDK stays dependency-free.

use crate::abi_v1::{ServiceReply, ServiceStatus};

/// The closed service-level result code set.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ServiceCode {
    Ok,
    Denied,
    NotFound,
    Conflict,
    Invalid,
    Limit,
    Fault,
}

impl ServiceCode {
    /// Parses the exact wire spelling of one code.
    pub fn from_bytes(value: &[u8]) -> Option<Self> {
        match value {
            b"ok" => Some(Self::Ok),
            b"denied" => Some(Self::Denied),
            b"not-found" => Some(Self::NotFound),
            b"conflict" => Some(Self::Conflict),
            b"invalid" => Some(Self::Invalid),
            b"limit" => Some(Self::Limit),
            b"fault" => Some(Self::Fault),
            _ => None,
        }
    }

    /// Returns the wire spelling.
    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Ok => "ok",
            Self::Denied => "denied",
            Self::NotFound => "not-found",
            Self::Conflict => "conflict",
            Self::Invalid => "invalid",
            Self::Limit => "limit",
            Self::Fault => "fault",
        }
    }
}

/// One parsed service response envelope. Slices borrow the reply payload.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Envelope<'a> {
    /// The service-level code.
    pub code: ServiceCode,
    /// The optional human-readable message.
    pub message: &'a str,
    /// The raw `data` value, without the surrounding field name. It is empty
    /// when the response has no data member.
    pub data: &'a [u8],
}

impl<'a> Envelope<'a> {
    /// Reports whether the service allowed the call.
    pub const fn is_ok(self) -> bool { matches!(self.code, ServiceCode::Ok) }

    /// Returns a string member of the data object.
    pub fn data_string(self, name: &str) -> Option<&'a str> {
        string_field(self.data, name)
    }

    /// Returns the raw value of a member of the data object.
    pub fn data_value(self, name: &str) -> Option<&'a [u8]> {
        value_field(self.data, name)
    }
}

/// One failure to interpret a service reply.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ServiceError {
    /// The transport layer returned a nonzero status.
    Transport(i32),
    /// The response was not a valid envelope.
    Malformed,
    /// The envelope named a code outside the closed vocabulary.
    UnknownCode,
}

impl ServiceError {
    /// Returns the transport status when this is a transport failure.
    pub const fn transport_status(self) -> Option<i32> {
        match self {
            Self::Transport(status) => Some(status),
            _ => None,
        }
    }
}

impl<'a> ServiceReply<'a> {
    /// Parses the JSON response envelope.
    pub fn envelope(&self) -> Result<Envelope<'a>, ServiceError> {
        if !self.is_ok() {
            return Err(ServiceError::Transport(self.status));
        }
        let code_bytes = string_field(self.payload, "code").ok_or(ServiceError::Malformed)?;
        let code = ServiceCode::from_bytes(code_bytes.as_bytes())
            .ok_or(ServiceError::UnknownCode)?;
        Ok(Envelope {
            code,
            message: string_field(self.payload, "message").unwrap_or(""),
            data: value_field(self.payload, "data").unwrap_or(&[]),
        })
    }
}

/// Reports whether one payload is a successful envelope.
pub fn is_ok(payload: &[u8]) -> bool {
    matches!(
        string_field(payload, "code").and_then(|code| ServiceCode::from_bytes(code.as_bytes())),
        Some(ServiceCode::Ok)
    )
}

/// Reports whether one transport status is zero.
pub const fn transport_ok(status: i32) -> bool { status == ServiceStatus::Ok.code() }

/// Returns a string member of a JSON object. Escapes are not decoded because
/// the host envelope uses plain ASCII messages and codes.
pub fn string_field<'a>(data: &'a [u8], name: &str) -> Option<&'a str> {
    let raw = value_field(data, name)?;
    let text = raw.strip_prefix(b"\"")?.strip_suffix(b"\"")?;
    core::str::from_utf8(text).ok()
}

/// Returns the raw JSON value of an object member, including quotes for a
/// string and brackets for an object or array.
pub fn value_field<'a>(data: &'a [u8], name: &str) -> Option<&'a [u8]> {
    let pattern = name.as_bytes();
    let mut index = 0;
    while index < data.len() {
        if data[index] != b'"' {
            index += 1;
            continue;
        }
        let name_start = index + 1;
        let name_end = name_start.checked_add(pattern.len())?;
        if name_end >= data.len() || &data[name_start..name_end] != pattern {
            index += 1;
            continue;
        }
        if data[name_end] != b'"' {
            index += 1;
            continue;
        }
        let mut cursor = name_end + 1;
        while cursor < data.len() && data[cursor].is_ascii_whitespace() {
            cursor += 1;
        }
        if cursor >= data.len() || data[cursor] != b':' {
            index += 1;
            continue;
        }
        cursor += 1;
        while cursor < data.len() && data[cursor].is_ascii_whitespace() {
            cursor += 1;
        }
        return Some(value_at(data, cursor));
    }
    None
}

/// Returns the value starting at cursor and ending at its structural
/// delimiter. The caller has already skipped whitespace.
fn value_at(data: &[u8], start: usize) -> &[u8] {
    if start >= data.len() {
        return &[];
    }
    match data[start] {
        b'"' => {
            let mut cursor = start + 1;
            while cursor < data.len() {
                match data[cursor] {
                    b'\\' => cursor += 2,
                    b'"' => return &data[start..=cursor],
                    _ => cursor += 1,
                }
            }
            &data[start..]
        }
        b'{' | b'[' => {
            let mut depth = 0i32;
            let mut cursor = start;
            while cursor < data.len() {
                match data[cursor] {
                    b'"' => {
                        cursor += 1;
                        while cursor < data.len() {
                            match data[cursor] {
                                b'\\' => cursor += 2,
                                b'"' => break,
                                _ => cursor += 1,
                            }
                        }
                    }
                    b'{' | b'[' => depth += 1,
                    b'}' | b']' => {
                        depth -= 1;
                        if depth == 0 {
                            return &data[start..=cursor];
                        }
                    }
                    _ => {}
                }
                cursor += 1;
            }
            &data[start..]
        }
        _ => {
            let mut cursor = start;
            while cursor < data.len() && data[cursor] != b',' && data[cursor] != b'}' {
                cursor += 1;
            }
            &data[start..cursor]
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_a_nested_envelope() {
        let payload = br#"{"code":"ok","message":"fine, thanks","data":{"value":"x"},"extra":{"a":[1,2]}}"#;
        let code = string_field(payload, "code").unwrap();
        assert_eq!(ServiceCode::from_bytes(code.as_bytes()), Some(ServiceCode::Ok));
        assert_eq!(string_field(payload, "message"), Some("fine, thanks"));
        let data = value_field(payload, "data").unwrap();
        assert_eq!(string_field(data, "value"), Some("x"));
        assert_eq!(value_field(payload, "extra"), Some(&br#"{"a":[1,2]}"#[..]));
    }
}
