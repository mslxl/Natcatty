//! Rust reference SDK for Natcatty plugins.
//!
//! `abi_v1` is generated from `sdk/idl/natcatty_v1.idl` and checked in. It
//! declares every host import and guest export, the typed ABI check, the
//! per-method service wrappers, the generation-tagged handles, and the native
//! UI enum vocabulary. `ensure_compatible_abi` is the init-time guard every
//! plugin should call.
//!
//! The hand-written modules add the typed helpers an author uses:
//!
//! - [`service`] parses the host-service JSON envelope without a dependency;
//! - [`stream`] wraps bounded connection-provider input and output queues;
//! - [`event`] decodes native UI event frames in host queue order;
//! - [`ui`] builds atomic node batches and decodes apply/listen/poll replies;
//! - [`mock`] (native builds only) installs the in-language mock host backed
//!   by the same shared stub definition as the CLI fake host.

pub mod abi_v1;
pub mod event;
#[cfg(not(target_arch = "wasm32"))]
pub mod mock;
pub mod service;
pub mod stream;
pub mod stub_gen;
pub mod ui;

pub use abi_v1::*;
pub use event::{decode_events, Event};
pub use service::{Envelope, ServiceCode, ServiceError};
pub use stream::{ProviderStream, StreamError};
pub use ui::{BatchBuilder, NodeBuilder, Value};

#[cfg(not(target_arch = "wasm32"))]
pub use mock::{MockHandle, MockHost, MockState};
