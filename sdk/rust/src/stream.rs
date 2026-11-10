//! Typed connection-provider streams.
//!
//! A provider never owns terminal state. It reads bounded input chunks and
//! writes bounded output chunks through the host-owned queues, respecting the
//! credit windows. All values are opaque bytes; encoding is the provider
//! protocol's business.

use crate::abi_v1;

/// One host-owned provider stream.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct ProviderStream {
    /// The stream id the host included in the provider request.
    pub id: u32,
}

/// The outcome of one cooperative pump turn.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct PumpResult {
    /// Bytes moved from input to output.
    pub moved: usize,
    /// The provider stopped because the budget was exhausted.
    pub budget_exhausted: bool,
    /// The provider stopped because no input was available.
    pub input_exhausted: bool,
    /// The provider stopped because output credit ran out.
    pub output_blocked: bool,
}

/// One stream operation failure.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum StreamError {
    /// The host reported no input or output credit.
    NoCredit,
    /// The host rejected a read.
    Read(i32),
    /// The host rejected a write.
    Write(i32),
}

impl ProviderStream {
    /// Wraps one stream id.
    pub const fn new(id: u32) -> Self { Self { id } }

    /// Reads available input into the caller's buffer. A non-positive return
    /// means no bytes are currently available.
    pub fn read(&self, buffer: &mut [u8]) -> i32 {
        abi_v1::provider_input_read(self.id, buffer)
    }

    /// Returns the input credit in bytes.
    pub fn input_credit(&self) -> i32 {
        abi_v1::provider_input_credit(self.id)
    }

    /// Writes one output chunk. Zero means accepted.
    pub fn write(&self, data: &[u8]) -> i32 {
        abi_v1::provider_output_write(self.id, data)
    }

    /// Returns the output credit in bytes.
    pub fn output_credit(&self) -> i32 {
        abi_v1::provider_output_credit(self.id)
    }

    /// Reads up to `max` available bytes without blocking.
    pub fn read_available(&self, max: usize) -> Result<Vec<u8>, StreamError> {
        if max == 0 {
            return Ok(Vec::new());
        }
        let credit = self.input_credit();
        if credit < 0 {
            return Err(StreamError::NoCredit);
        }
        let capacity = (credit as usize).min(max);
        if capacity == 0 {
            return Ok(Vec::new());
        }
        let mut buffer = vec![0u8; capacity];
        let read = self.read(&mut buffer);
        if read < 0 {
            return Err(StreamError::Read(read));
        }
        buffer.truncate(read as usize);
        Ok(buffer)
    }

    /// Moves input to output until the budget is exhausted, input runs dry,
    /// or the host stops granting output credit. A write rejected after the
    /// read reports the host status and consumes that read chunk; the caller
    /// must treat the pump as failed rather than retrying the same bytes.
    pub fn pump(&self, budget: usize, chunk: usize) -> Result<PumpResult, StreamError> {
        let chunk = chunk.max(1);
        let mut result = PumpResult {
            moved: 0,
            budget_exhausted: false,
            input_exhausted: false,
            output_blocked: false,
        };
        let mut buffer = vec![0u8; chunk];
        while result.moved < budget {
            let credit = self.output_credit();
            if credit < 0 {
                return Err(StreamError::NoCredit);
            }
            if credit == 0 {
                result.output_blocked = true;
                break;
            }
            let capacity = (credit as usize).min(chunk).min(budget - result.moved);
            let read = self.read(&mut buffer[..capacity]);
            if read < 0 {
                return Err(StreamError::Read(read));
            }
            if read == 0 {
                result.input_exhausted = true;
                break;
            }
            let written = self.write(&buffer[..read as usize]);
            if written != 0 {
                return Err(StreamError::Write(written));
            }
            result.moved += read as usize;
        }
        if result.moved >= budget {
            result.budget_exhausted = true;
        }
        Ok(result)
    }
}
