//! Sample command plugin.
//!
//! The generated bindings define `natcatty_abi_version` and the typed
//! `ensure_compatible_abi` check. Init rejects an incompatible host before the
//! plugin publishes anything; dispatch produces visible output through the
//! host's `output_write` import.

use natcatty_plugin_sdk::abi_v1 as abi;

static mut HEAP: [u8; 4096] = [0; 4096];
static mut HEAP_USED: u32 = 0;

/// Allocates command input in the module's static heap. The host copies the
/// command bytes here before calling dispatch.
#[no_mangle]
pub extern "C" fn natcatty_alloc(size: u32) -> u32 {
    unsafe {
        let ptr = core::ptr::addr_of_mut!(HEAP) as *mut u8 as u32 + HEAP_USED;
        HEAP_USED += (size + 7) & !7;
        ptr
    }
}

#[no_mangle]
pub extern "C" fn natcatty_init() -> i32 {
    if let Err(err) = abi::ensure_compatible_abi() {
        abi::log(abi::LogLevel::Error, err.to_string().as_bytes());
        return -1;
    }
    abi::log(abi::LogLevel::Info, b"hello init");
    0
}

#[no_mangle]
pub extern "C" fn natcatty_dispatch(_command_ptr: u32, _command_len: u32) -> i32 {
    let mut random = [0u8; 8];
    if abi::random_get(&mut random) != 0 {
        return -1;
    }
    abi::log(abi::LogLevel::Info, b"dispatch");
    let mut message = String::from("hello from plugin now=");
    message.push_str(&abi::now_ns().to_string());
    message.push_str(" rand=");
    for byte in random {
        message.push_str(&format!("{byte:02x}"));
    }
    if abi::output_write(message.as_bytes()) < 0 {
        -1
    } else {
        0
    }
}

#[no_mangle]
pub extern "C" fn natcatty_poll() -> i32 {
    0
}

#[no_mangle]
pub extern "C" fn natcatty_shutdown() -> i32 {
    0
}
