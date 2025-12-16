#include <node_api.h>

#include <windows.h>
#include <objbase.h>
#include <wincrypt.h>
#include <bcrypt.h>
#include <webauthn.h>

#include <memory>
#include <string>
#include <vector>
#include <mutex>

#pragma comment(lib, "bcrypt.lib")
#pragma comment(lib, "webauthn.lib")

namespace {

constexpr const wchar_t* kOriginPrefix = L"https://";

struct AsyncContext {
  napi_env env{nullptr};
  napi_async_work work{nullptr};
  napi_deferred deferred{nullptr};

  std::string error;
  bool bool_result{false};
  std::vector<uint8_t> buffer_result;

  std::wstring rp_id;
  std::wstring rp_name;
  std::wstring user_name;
  HWND parent_hwnd{nullptr};
  std::vector<uint8_t> credential_id;

  bool do_is_available{false};
  bool do_make_credential{false};
  bool do_get_assertion{false};
};

struct ScopedCoInit {
  HRESULT hr{E_FAIL};
  bool did_init{false};
  ScopedCoInit() {
    // WebAuthn APIs can rely on COM/RPC; initialize COM on the calling thread.
    hr = CoInitializeEx(nullptr, COINIT_APARTMENTTHREADED);
    if (hr == RPC_E_CHANGED_MODE) {
      hr = CoInitializeEx(nullptr, COINIT_MULTITHREADED);
    }
    did_init = SUCCEEDED(hr);
  }
  ~ScopedCoInit() {
    if (did_init) CoUninitialize();
  }
};

HRESULT EnsureComSecurityInitialized() {
  static std::once_flag once;
  static HRESULT result = E_FAIL;
  std::call_once(once, []() {
    // Some brokered COM/RPC paths require explicit security init; otherwise calls can fail
    // with RPC_E_ACCESS_DENIED.
    result = CoInitializeSecurity(nullptr,
                                  -1,
                                  nullptr,
                                  nullptr,
                                  RPC_C_AUTHN_LEVEL_DEFAULT,
                                  RPC_C_IMP_LEVEL_IMPERSONATE,
                                  nullptr,
                                  EOAC_NONE,
                                  nullptr);
    if (result == RPC_E_TOO_LATE) {
      // Already initialized by the host process.
      result = S_OK;
    }
  });
  return result;
}

// Helper struct for EnumWindows callback
struct EnumWindowsData {
  DWORD targetPid;
  HWND foundHwnd;
};

// Get a valid HWND for WebAuthn operations.
// If the provided hwnd is invalid, try to find a suitable alternative.
HWND GetValidHwndForWebAuthn(HWND hwnd) {
  // If provided hwnd is valid, use it
  if (hwnd && IsWindow(hwnd)) {
    return hwnd;
  }
  
  // Try to get the foreground window
  HWND fg = GetForegroundWindow();
  if (fg && IsWindow(fg)) {
    return fg;
  }
  
  // Try to get the active window
  HWND active = GetActiveWindow();
  if (active && IsWindow(active)) {
    return active;
  }
  
  // Try to find our process's top-level window
  EnumWindowsData data = { GetCurrentProcessId(), nullptr };
  EnumWindows([](HWND hwnd, LPARAM lParam) -> BOOL {
    auto* pData = reinterpret_cast<EnumWindowsData*>(lParam);
    DWORD windowPid = 0;
    GetWindowThreadProcessId(hwnd, &windowPid);
    if (windowPid == pData->targetPid) {
      if (IsWindowVisible(hwnd)) {
        // Found a visible window in our process
        pData->foundHwnd = hwnd;
        return FALSE;  // Stop enumeration
      }
    }
    return TRUE;  // Continue enumeration
  }, reinterpret_cast<LPARAM>(&data));
  
  if (data.foundHwnd && IsWindow(data.foundHwnd)) {
    return data.foundHwnd;
  }
  
  // Last resort: use nullptr and let Windows pick (not ideal but better than nothing)
  // Note: GetDesktopWindow() typically doesn't work well for WebAuthn
  return nullptr;
}

void BringToForeground(HWND hwnd) {
  if (!hwnd) return;
  if (!IsWindow(hwnd)) return;

  // Best-effort: make our parent window foreground so the Windows Security UI
  // isn't rejected for being invoked from a background app.
  HWND fg = GetForegroundWindow();
  const DWORD fgThread = fg ? GetWindowThreadProcessId(fg, nullptr) : 0;
  const DWORD hwndThread = GetWindowThreadProcessId(hwnd, nullptr);
  const DWORD curThread = GetCurrentThreadId();

  if (fgThread && fgThread != curThread) AttachThreadInput(fgThread, curThread, TRUE);
  if (hwndThread && hwndThread != curThread) AttachThreadInput(hwndThread, curThread, TRUE);

  AllowSetForegroundWindow(ASFW_ANY);
  
  // Restore window if minimized
  if (IsIconic(hwnd)) {
    ShowWindow(hwnd, SW_RESTORE);
  }
  
  // Ensure window is visible
  ShowWindow(hwnd, SW_SHOW);
  
  // Bring to foreground using multiple approaches for reliability
  SetForegroundWindow(hwnd);
  BringWindowToTop(hwnd);
  SetActiveWindow(hwnd);
  SetFocus(hwnd);
  
  // Double-check: if we're still not foreground, try again
  if (GetForegroundWindow() != hwnd) {
    // Use keybd_event trick to allow SetForegroundWindow from background
    keybd_event(0, 0, 0, 0);
    keybd_event(0, 0, KEYEVENTF_KEYUP, 0);
    SetForegroundWindow(hwnd);
  }

  if (hwndThread && hwndThread != curThread) AttachThreadInput(hwndThread, curThread, FALSE);
  if (fgThread && fgThread != curThread) AttachThreadInput(fgThread, curThread, FALSE);
}

std::string WideToUtf8(const std::wstring& w) {
  if (w.empty()) return {};
  const int size =
      WideCharToMultiByte(CP_UTF8, 0, w.c_str(), static_cast<int>(w.size()), nullptr, 0, nullptr, nullptr);
  std::string out(size, '\0');
  WideCharToMultiByte(CP_UTF8, 0, w.c_str(), static_cast<int>(w.size()), out.data(), size, nullptr, nullptr);
  return out;
}

std::wstring Utf8ToWide(const std::string& s) {
  if (s.empty()) return {};
  const int size = MultiByteToWideChar(CP_UTF8, 0, s.c_str(), static_cast<int>(s.size()), nullptr, 0);
  std::wstring out(size, L'\0');
  MultiByteToWideChar(CP_UTF8, 0, s.c_str(), static_cast<int>(s.size()), out.data(), size);
  return out;
}

napi_value MakeError(napi_env env, const std::string& message) {
  napi_value msg;
  napi_create_string_utf8(env, message.c_str(), message.size(), &msg);
  napi_value err;
  napi_create_error(env, nullptr, msg, &err);
  return err;
}

std::string HResultToString(HRESULT hr) {
  char code[64];
  sprintf_s(code, "HRESULT=0x%08X", static_cast<unsigned int>(hr));

  wchar_t* msgBuf = nullptr;
  const DWORD flags = FORMAT_MESSAGE_ALLOCATE_BUFFER | FORMAT_MESSAGE_FROM_SYSTEM | FORMAT_MESSAGE_IGNORE_INSERTS;
  const DWORD langId = MAKELANGID(LANG_NEUTRAL, SUBLANG_DEFAULT);
  const DWORD len = FormatMessageW(flags, nullptr, static_cast<DWORD>(hr), langId,
                                   reinterpret_cast<LPWSTR>(&msgBuf), 0, nullptr);
  if (!len || !msgBuf) return std::string(code);

  std::wstring wmsg(msgBuf, msgBuf + len);
  LocalFree(msgBuf);
  while (!wmsg.empty() && (wmsg.back() == L'\r' || wmsg.back() == L'\n' || wmsg.back() == L' ')) wmsg.pop_back();
  const auto msg = WideToUtf8(wmsg);
  return msg.empty() ? std::string(code) : (std::string(code) + " (" + msg + ")");
}

bool GetRandomBytes(std::vector<uint8_t>& out, size_t len) {
  out.resize(len);
  return BCryptGenRandom(nullptr, out.data(), static_cast<ULONG>(out.size()), BCRYPT_USE_SYSTEM_PREFERRED_RNG) == 0;
}

std::string Base64UrlEncode(const std::vector<uint8_t>& data) {
  if (data.empty()) return {};
  DWORD outLen = 0;
  if (!CryptBinaryToStringA(data.data(), static_cast<DWORD>(data.size()),
                            CRYPT_STRING_BASE64 | CRYPT_STRING_NOCRLF, nullptr, &outLen)) {
    return {};
  }
  std::string base64(outLen, '\0');
  if (!CryptBinaryToStringA(data.data(), static_cast<DWORD>(data.size()),
                            CRYPT_STRING_BASE64 | CRYPT_STRING_NOCRLF, base64.data(), &outLen)) {
    return {};
  }
  if (!base64.empty() && base64.back() == '\0') base64.pop_back();
  // base64url transform
  for (auto& ch : base64) {
    if (ch == '+') ch = '-';
    else if (ch == '/') ch = '_';
  }
  while (!base64.empty() && base64.back() == '=') base64.pop_back();
  return base64;
}

std::vector<uint8_t> MakeClientDataJson(const std::string& type,
                                        const std::wstring& rpId,
                                        const std::vector<uint8_t>& challenge) {
  const auto challengeB64 = Base64UrlEncode(challenge);
  const auto origin = WideToUtf8(std::wstring(kOriginPrefix) + rpId);

  std::string json = "{";
  json += "\"type\":\"" + type + "\",";
  json += "\"challenge\":\"" + challengeB64 + "\",";
  json += "\"origin\":\"" + origin + "\",";
  json += "\"crossOrigin\":false";
  json += "}";

  return std::vector<uint8_t>(json.begin(), json.end());
}

HWND ReadHwndFromBuffer(void* data, size_t data_len) {
  if (!data) return nullptr;
  if (data_len != sizeof(void*) && data_len != sizeof(uint64_t)) return nullptr;
  HWND hwnd = nullptr;
  memcpy(&hwnd, data, sizeof(void*));
  return hwnd;
}

void Execute(napi_env /*env*/, void* data) {
  auto* ctx = static_cast<AsyncContext*>(data);

  if (ctx->do_is_available) {
    BOOL available = FALSE;
    const HRESULT hr = WebAuthNIsUserVerifyingPlatformAuthenticatorAvailable(&available);
    if (FAILED(hr)) {
      ctx->error = "WebAuthNIsUserVerifyingPlatformAuthenticatorAvailable failed: " + HResultToString(hr);
      return;
    }
    ctx->bool_result = (available == TRUE);
    return;
  }

  if (ctx->rp_id.empty()) {
    ctx->error = "rpId is required";
    return;
  }

  if (ctx->do_make_credential) {
    std::vector<uint8_t> challenge;
    std::vector<uint8_t> userId;
    if (!GetRandomBytes(challenge, 32) || !GetRandomBytes(userId, 32)) {
      ctx->error = "Failed to generate random bytes";
      return;
    }

    const auto clientDataJson = MakeClientDataJson("webauthn.create", ctx->rp_id, challenge);

    WEBAUTHN_RP_ENTITY_INFORMATION rpInfo{};
    rpInfo.dwVersion = WEBAUTHN_RP_ENTITY_INFORMATION_CURRENT_VERSION;
    rpInfo.pwszId = ctx->rp_id.c_str();
    rpInfo.pwszName = ctx->rp_name.empty() ? ctx->rp_id.c_str() : ctx->rp_name.c_str();

    WEBAUTHN_USER_ENTITY_INFORMATION userInfo{};
    userInfo.dwVersion = WEBAUTHN_USER_ENTITY_INFORMATION_CURRENT_VERSION;
    userInfo.cbId = static_cast<DWORD>(userId.size());
    userInfo.pbId = userId.data();
    userInfo.pwszName = ctx->user_name.empty() ? L"Netcatty" : ctx->user_name.c_str();
    userInfo.pwszDisplayName = ctx->user_name.empty() ? L"Netcatty" : ctx->user_name.c_str();

    WEBAUTHN_COSE_CREDENTIAL_PARAMETER coseParams[2]{};
    coseParams[0].dwVersion = WEBAUTHN_COSE_CREDENTIAL_PARAMETER_CURRENT_VERSION;
    coseParams[0].pwszCredentialType = WEBAUTHN_CREDENTIAL_TYPE_PUBLIC_KEY;
    coseParams[0].lAlg = WEBAUTHN_COSE_ALGORITHM_ECDSA_P256_WITH_SHA256;  // ES256

    coseParams[1].dwVersion = WEBAUTHN_COSE_CREDENTIAL_PARAMETER_CURRENT_VERSION;
    coseParams[1].pwszCredentialType = WEBAUTHN_CREDENTIAL_TYPE_PUBLIC_KEY;
    coseParams[1].lAlg = WEBAUTHN_COSE_ALGORITHM_RSASSA_PKCS1_V1_5_WITH_SHA256;  // RS256

    WEBAUTHN_COSE_CREDENTIAL_PARAMETERS coseList{};
    coseList.cCredentialParameters = 2;
    coseList.pCredentialParameters = coseParams;

    WEBAUTHN_CLIENT_DATA clientData{};
    clientData.dwVersion = WEBAUTHN_CLIENT_DATA_CURRENT_VERSION;
    clientData.cbClientDataJSON = static_cast<DWORD>(clientDataJson.size());
    clientData.pbClientDataJSON = const_cast<PBYTE>(clientDataJson.data());
    clientData.pwszHashAlgId = WEBAUTHN_HASH_ALGORITHM_SHA_256;

    WEBAUTHN_AUTHENTICATOR_MAKE_CREDENTIAL_OPTIONS options{};
    options.dwVersion = WEBAUTHN_AUTHENTICATOR_MAKE_CREDENTIAL_OPTIONS_CURRENT_VERSION;
    options.dwTimeoutMilliseconds = 60'000;
    options.dwUserVerificationRequirement = WEBAUTHN_USER_VERIFICATION_REQUIREMENT_REQUIRED;
    options.dwAttestationConveyancePreference = WEBAUTHN_ATTESTATION_CONVEYANCE_PREFERENCE_NONE;
    // Prefer the built-in Windows Hello platform authenticator (system passkey store),
    // rather than prompting for an external security key by default.
    options.dwAuthenticatorAttachment = WEBAUTHN_AUTHENTICATOR_ATTACHMENT_PLATFORM;

    PWEBAUTHN_CREDENTIAL_ATTESTATION attestation = nullptr;
    const HRESULT hr = WebAuthNAuthenticatorMakeCredential(ctx->parent_hwnd,
                                                           &rpInfo,
                                                           &userInfo,
                                                           &coseList,
                                                           &clientData,
                                                           &options,
                                                           &attestation);
    if (FAILED(hr)) {
      ctx->error = "WebAuthNAuthenticatorMakeCredential failed: " + HResultToString(hr);
      if (attestation) WebAuthNFreeCredentialAttestation(attestation);
      return;
    }

    if (!attestation || !attestation->pbCredentialId || attestation->cbCredentialId == 0) {
      ctx->error = "Credential attestation missing credentialId";
      if (attestation) WebAuthNFreeCredentialAttestation(attestation);
      return;
    }

    ctx->buffer_result.assign(attestation->pbCredentialId, attestation->pbCredentialId + attestation->cbCredentialId);
    WebAuthNFreeCredentialAttestation(attestation);
    return;
  }

  if (ctx->do_get_assertion) {
    if (ctx->credential_id.empty()) {
      ctx->error = "credentialId is required";
      return;
    }

    std::vector<uint8_t> challenge;
    if (!GetRandomBytes(challenge, 32)) {
      ctx->error = "Failed to generate random challenge";
      return;
    }
    const auto clientDataJson = MakeClientDataJson("webauthn.get", ctx->rp_id, challenge);

    WEBAUTHN_CLIENT_DATA clientData{};
    clientData.dwVersion = WEBAUTHN_CLIENT_DATA_CURRENT_VERSION;
    clientData.cbClientDataJSON = static_cast<DWORD>(clientDataJson.size());
    clientData.pbClientDataJSON = const_cast<PBYTE>(clientDataJson.data());
    clientData.pwszHashAlgId = WEBAUTHN_HASH_ALGORITHM_SHA_256;

    WEBAUTHN_CREDENTIAL cred{};
    cred.dwVersion = WEBAUTHN_CREDENTIAL_CURRENT_VERSION;
    cred.cbId = static_cast<DWORD>(ctx->credential_id.size());
    cred.pbId = ctx->credential_id.data();
    cred.pwszCredentialType = WEBAUTHN_CREDENTIAL_TYPE_PUBLIC_KEY;

    WEBAUTHN_CREDENTIALS allowList{};
    allowList.cCredentials = 1;
    allowList.pCredentials = &cred;

    WEBAUTHN_AUTHENTICATOR_GET_ASSERTION_OPTIONS options{};
    options.dwVersion = WEBAUTHN_AUTHENTICATOR_GET_ASSERTION_OPTIONS_CURRENT_VERSION;
    options.dwTimeoutMilliseconds = 60'000;
    options.dwUserVerificationRequirement = WEBAUTHN_USER_VERIFICATION_REQUIREMENT_REQUIRED;
    options.CredentialList = allowList;
    // Match the make-credential preference so assertions use the Windows Hello platform authenticator.
    options.dwAuthenticatorAttachment = WEBAUTHN_AUTHENTICATOR_ATTACHMENT_PLATFORM;

    PWEBAUTHN_ASSERTION assertion = nullptr;
    const HRESULT hr = WebAuthNAuthenticatorGetAssertion(ctx->parent_hwnd,
                                                         ctx->rp_id.c_str(),
                                                         &clientData,
                                                         &options,
                                                         &assertion);
    if (FAILED(hr)) {
      ctx->error = "WebAuthNAuthenticatorGetAssertion failed: " + HResultToString(hr);
      if (assertion) WebAuthNFreeAssertion(assertion);
      return;
    }

    ctx->bool_result = true;
    if (assertion) WebAuthNFreeAssertion(assertion);
    return;
  }

  ctx->error = "Invalid operation";
}

void Complete(napi_env env, napi_status /*status*/, void* data) {
  std::unique_ptr<AsyncContext> ctx(static_cast<AsyncContext*>(data));

  if (!ctx->error.empty()) {
    napi_reject_deferred(env, ctx->deferred, MakeError(env, ctx->error));
  } else if (!ctx->buffer_result.empty()) {
    napi_value buf;
    void* outData = nullptr;
    napi_create_buffer_copy(env, ctx->buffer_result.size(), ctx->buffer_result.data(), &outData, &buf);
    napi_resolve_deferred(env, ctx->deferred, buf);
  } else {
    napi_value v;
    napi_get_boolean(env, ctx->bool_result, &v);
    napi_resolve_deferred(env, ctx->deferred, v);
  }

  napi_delete_async_work(env, ctx->work);
}

bool GetStringArg(napi_env env, napi_value v, std::string& out) {
  napi_valuetype t;
  napi_typeof(env, v, &t);
  if (t != napi_string) return false;
  size_t len = 0;
  napi_get_value_string_utf8(env, v, nullptr, 0, &len);
  out.resize(len);
  size_t written = 0;
  napi_get_value_string_utf8(env, v, out.data(), out.size() + 1, &written);
  out.resize(written);
  return true;
}

bool GetBufferArg(napi_env env, napi_value v, std::vector<uint8_t>& out) {
  bool isBuf = false;
  napi_is_buffer(env, v, &isBuf);
  if (!isBuf) return false;
  void* data = nullptr;
  size_t len = 0;
  napi_get_buffer_info(env, v, &data, &len);
  out.assign(reinterpret_cast<uint8_t*>(data), reinterpret_cast<uint8_t*>(data) + len);
  return true;
}

napi_value IsAvailableSync(napi_env env, napi_callback_info info) {
  size_t argc = 0;
  napi_value argv[1];
  napi_get_cb_info(env, info, &argc, argv, nullptr, nullptr);

  ScopedCoInit co;
  if (!co.did_init) {
    napi_throw_error(env, nullptr, "COM initialization failed");
    return nullptr;
  }
  const HRESULT sec = EnsureComSecurityInitialized();
  if (FAILED(sec)) {
    napi_throw_error(env, nullptr, ("CoInitializeSecurity failed: " + HResultToString(sec)).c_str());
    return nullptr;
  }

  BOOL available = FALSE;
  const HRESULT hr = WebAuthNIsUserVerifyingPlatformAuthenticatorAvailable(&available);
  if (FAILED(hr)) {
    napi_throw_error(env, nullptr, ("WebAuthNIsUserVerifyingPlatformAuthenticatorAvailable failed: " + HResultToString(hr)).c_str());
    return nullptr;
  }

  napi_value v;
  napi_get_boolean(env, available ? true : false, &v);
  return v;
}

napi_value CreateCredentialSync(napi_env env, napi_callback_info info) {
  size_t argc = 4;
  napi_value argv[4];
  napi_get_cb_info(env, info, &argc, argv, nullptr, nullptr);

  std::string rpId;
  std::string rpName;
  std::string userName;
  if (argc < 3 || !GetStringArg(env, argv[0], rpId) || !GetStringArg(env, argv[1], rpName) ||
      !GetStringArg(env, argv[2], userName)) {
    napi_throw_type_error(env, nullptr, "Expected (rpId: string, rpName: string, userName: string, hwnd?: Buffer)");
    return nullptr;
  }

  HWND hwnd = nullptr;
  if (argc >= 4) {
    bool isBuf = false;
    napi_is_buffer(env, argv[3], &isBuf);
    if (isBuf) {
      void* data = nullptr;
      size_t len = 0;
      napi_get_buffer_info(env, argv[3], &data, &len);
      hwnd = ReadHwndFromBuffer(data, len);
    }
  }
  
  // Ensure we have a valid HWND - WebAuthn requires this
  hwnd = GetValidHwndForWebAuthn(hwnd);

  ScopedCoInit co;
  if (!co.did_init) {
    napi_throw_error(env, nullptr, "COM initialization failed");
    return nullptr;
  }
  const HRESULT sec = EnsureComSecurityInitialized();
  if (FAILED(sec)) {
    napi_throw_error(env, nullptr, ("CoInitializeSecurity failed: " + HResultToString(sec)).c_str());
    return nullptr;
  }

  BringToForeground(hwnd);

  std::vector<uint8_t> challenge;
  if (!GetRandomBytes(challenge, 32)) {
    napi_throw_error(env, nullptr, "Failed to generate random challenge");
    return nullptr;
  }
  std::vector<uint8_t> userId;
  if (!GetRandomBytes(userId, 32)) {
    napi_throw_error(env, nullptr, "Failed to generate random userId");
    return nullptr;
  }

  const auto rpIdW = Utf8ToWide(rpId);
  const auto rpNameW = Utf8ToWide(rpName);
  const auto userNameW = Utf8ToWide(userName);
  const auto clientDataJson = MakeClientDataJson("webauthn.create", rpIdW, challenge);

  WEBAUTHN_RP_ENTITY_INFORMATION rpInfo{};
  rpInfo.dwVersion = WEBAUTHN_RP_ENTITY_INFORMATION_CURRENT_VERSION;
  rpInfo.pwszId = rpIdW.c_str();
  rpInfo.pwszName = rpNameW.empty() ? rpIdW.c_str() : rpNameW.c_str();

  WEBAUTHN_USER_ENTITY_INFORMATION userInfo{};
  userInfo.dwVersion = WEBAUTHN_USER_ENTITY_INFORMATION_CURRENT_VERSION;
  userInfo.cbId = static_cast<DWORD>(userId.size());
  userInfo.pbId = userId.data();
  userInfo.pwszName = userNameW.empty() ? L"Netcatty" : userNameW.c_str();
  userInfo.pwszDisplayName = userNameW.empty() ? L"Netcatty" : userNameW.c_str();

  WEBAUTHN_COSE_CREDENTIAL_PARAMETER coseParams[2]{};
  coseParams[0].dwVersion = WEBAUTHN_COSE_CREDENTIAL_PARAMETER_CURRENT_VERSION;
  coseParams[0].pwszCredentialType = WEBAUTHN_CREDENTIAL_TYPE_PUBLIC_KEY;
  coseParams[0].lAlg = WEBAUTHN_COSE_ALGORITHM_ECDSA_P256_WITH_SHA256;  // ES256

  coseParams[1].dwVersion = WEBAUTHN_COSE_CREDENTIAL_PARAMETER_CURRENT_VERSION;
  coseParams[1].pwszCredentialType = WEBAUTHN_CREDENTIAL_TYPE_PUBLIC_KEY;
  coseParams[1].lAlg = WEBAUTHN_COSE_ALGORITHM_RSASSA_PKCS1_V1_5_WITH_SHA256;  // RS256

  WEBAUTHN_COSE_CREDENTIAL_PARAMETERS coseList{};
  coseList.cCredentialParameters = 2;
  coseList.pCredentialParameters = coseParams;

  WEBAUTHN_CLIENT_DATA clientData{};
  clientData.dwVersion = WEBAUTHN_CLIENT_DATA_CURRENT_VERSION;
  clientData.cbClientDataJSON = static_cast<DWORD>(clientDataJson.size());
  clientData.pbClientDataJSON = const_cast<PBYTE>(clientDataJson.data());
  clientData.pwszHashAlgId = WEBAUTHN_HASH_ALGORITHM_SHA_256;

  WEBAUTHN_AUTHENTICATOR_MAKE_CREDENTIAL_OPTIONS options{};
  options.dwVersion = WEBAUTHN_AUTHENTICATOR_MAKE_CREDENTIAL_OPTIONS_CURRENT_VERSION;
  options.dwTimeoutMilliseconds = 60'000;
  options.dwUserVerificationRequirement = WEBAUTHN_USER_VERIFICATION_REQUIREMENT_REQUIRED;
  options.dwAttestationConveyancePreference = WEBAUTHN_ATTESTATION_CONVEYANCE_PREFERENCE_NONE;
  options.dwAuthenticatorAttachment = WEBAUTHN_AUTHENTICATOR_ATTACHMENT_PLATFORM;

  // WebAuthn requires a valid foreground window to display the Windows Hello prompt.
  // The RPC_E_ACCESS_DENIED error occurs when the calling process is not in foreground.
  
  // First, allow the current process to set foreground window
  AllowSetForegroundWindow(ASFW_ANY);
  
  // Get the foreground window - this should be the Electron window
  HWND fgHwnd = GetForegroundWindow();
  
  // If we have a passed hwnd, try to bring it to foreground first
  if (hwnd && IsWindow(hwnd)) {
    // Force the window to foreground using keyboard simulation trick
    DWORD foregroundThread = GetWindowThreadProcessId(GetForegroundWindow(), nullptr);
    DWORD currentThread = GetCurrentThreadId();
    
    if (foregroundThread != currentThread) {
      AttachThreadInput(currentThread, foregroundThread, TRUE);
    }
    
    SetForegroundWindow(hwnd);
    BringWindowToTop(hwnd);
    SetFocus(hwnd);
    
    if (foregroundThread != currentThread) {
      AttachThreadInput(currentThread, foregroundThread, FALSE);
    }
    
    // Small delay to let Windows process the foreground change
    Sleep(100);
    
    // Re-get foreground window after our attempts
    fgHwnd = GetForegroundWindow();
  }
  
  // Determine which hwnd to use
  HWND webauthnHwnd = nullptr;
  
  // Prefer the passed hwnd if it's now the foreground, otherwise use foreground window
  if (hwnd && IsWindow(hwnd) && hwnd == fgHwnd) {
    webauthnHwnd = hwnd;
  } else if (fgHwnd && IsWindow(fgHwnd)) {
    webauthnHwnd = fgHwnd;
  } else if (hwnd && IsWindow(hwnd)) {
    webauthnHwnd = hwnd;
  }
  
  // Last resort: use desktop window (this usually doesn't work well but better than nullptr)
  if (!webauthnHwnd || !IsWindow(webauthnHwnd)) {
    webauthnHwnd = GetDesktopWindow();
  }
  
  // Log hwnd info for debugging
  char hwndInfo[512];
  snprintf(hwndInfo, sizeof(hwndInfo), 
           "[WebAuthn] CreateCredential - passedHwnd=0x%p, fgHwnd=0x%p, using=0x%p, AreSame=%d",
           (void*)hwnd, (void*)fgHwnd, (void*)webauthnHwnd, hwnd == fgHwnd);
  OutputDebugStringA(hwndInfo);

  PWEBAUTHN_CREDENTIAL_ATTESTATION attestation = nullptr;
  const HRESULT hr = WebAuthNAuthenticatorMakeCredential(webauthnHwnd, &rpInfo, &userInfo, &coseList, &clientData, &options,
                                                         &attestation);
  if (FAILED(hr)) {
    if (attestation) WebAuthNFreeCredentialAttestation(attestation);
    napi_throw_error(env, nullptr, ("WebAuthNAuthenticatorMakeCredential failed: " + HResultToString(hr)).c_str());
    return nullptr;
  }

  if (!attestation || !attestation->pbCredentialId || attestation->cbCredentialId == 0) {
    if (attestation) WebAuthNFreeCredentialAttestation(attestation);
    napi_throw_error(env, nullptr, "Credential attestation missing credentialId");
    return nullptr;
  }

  napi_value buf;
  void* outData = nullptr;
  napi_create_buffer_copy(env, attestation->cbCredentialId, attestation->pbCredentialId, &outData, &buf);
  WebAuthNFreeCredentialAttestation(attestation);
  return buf;
}

napi_value GetAssertionSync(napi_env env, napi_callback_info info) {
  size_t argc = 3;
  napi_value argv[3];
  napi_get_cb_info(env, info, &argc, argv, nullptr, nullptr);

  std::string rpId;
  std::vector<uint8_t> credId;
  if (argc < 2 || !GetStringArg(env, argv[0], rpId) || !GetBufferArg(env, argv[1], credId)) {
    napi_throw_type_error(env, nullptr, "Expected (rpId: string, credentialId: Buffer, hwnd?: Buffer)");
    return nullptr;
  }

  HWND hwnd = nullptr;
  if (argc >= 3) {
    bool isBuf = false;
    napi_is_buffer(env, argv[2], &isBuf);
    if (isBuf) {
      void* data = nullptr;
      size_t len = 0;
      napi_get_buffer_info(env, argv[2], &data, &len);
      hwnd = ReadHwndFromBuffer(data, len);
    }
  }
  
  // Ensure we have a valid HWND - WebAuthn requires this
  hwnd = GetValidHwndForWebAuthn(hwnd);

  ScopedCoInit co;
  if (!co.did_init) {
    napi_throw_error(env, nullptr, "COM initialization failed");
    return nullptr;
  }
  const HRESULT sec = EnsureComSecurityInitialized();
  if (FAILED(sec)) {
    napi_throw_error(env, nullptr, ("CoInitializeSecurity failed: " + HResultToString(sec)).c_str());
    return nullptr;
  }

  BringToForeground(hwnd);

  std::vector<uint8_t> challenge;
  if (!GetRandomBytes(challenge, 32)) {
    napi_throw_error(env, nullptr, "Failed to generate random challenge");
    return nullptr;
  }

  const auto rpIdW = Utf8ToWide(rpId);
  const auto clientDataJson = MakeClientDataJson("webauthn.get", rpIdW, challenge);

  WEBAUTHN_CLIENT_DATA clientData{};
  clientData.dwVersion = WEBAUTHN_CLIENT_DATA_CURRENT_VERSION;
  clientData.cbClientDataJSON = static_cast<DWORD>(clientDataJson.size());
  clientData.pbClientDataJSON = const_cast<PBYTE>(clientDataJson.data());
  clientData.pwszHashAlgId = WEBAUTHN_HASH_ALGORITHM_SHA_256;

  WEBAUTHN_CREDENTIAL cred{};
  cred.dwVersion = WEBAUTHN_CREDENTIAL_CURRENT_VERSION;
  cred.cbId = static_cast<DWORD>(credId.size());
  cred.pbId = credId.data();
  cred.pwszCredentialType = WEBAUTHN_CREDENTIAL_TYPE_PUBLIC_KEY;

  WEBAUTHN_CREDENTIALS allowList{};
  allowList.cCredentials = 1;
  allowList.pCredentials = &cred;

  WEBAUTHN_AUTHENTICATOR_GET_ASSERTION_OPTIONS options{};
  options.dwVersion = WEBAUTHN_AUTHENTICATOR_GET_ASSERTION_OPTIONS_CURRENT_VERSION;
  options.dwTimeoutMilliseconds = 60'000;
  options.dwUserVerificationRequirement = WEBAUTHN_USER_VERIFICATION_REQUIREMENT_REQUIRED;
  options.CredentialList = allowList;
  options.dwAuthenticatorAttachment = WEBAUTHN_AUTHENTICATOR_ATTACHMENT_PLATFORM;

  // Critical: Use GetForegroundWindow() to get the actual foreground window
  HWND fgHwnd = GetForegroundWindow();
  HWND webauthnHwnd = fgHwnd ? fgHwnd : hwnd;
  if (!webauthnHwnd || !IsWindow(webauthnHwnd)) {
    webauthnHwnd = GetActiveWindow();
  }
  if (!webauthnHwnd || !IsWindow(webauthnHwnd)) {
    webauthnHwnd = GetDesktopWindow();
  }

  PWEBAUTHN_ASSERTION assertion = nullptr;
  const HRESULT hr = WebAuthNAuthenticatorGetAssertion(webauthnHwnd, rpIdW.c_str(), &clientData, &options, &assertion);
  if (FAILED(hr)) {
    if (assertion) WebAuthNFreeAssertion(assertion);
    napi_throw_error(env, nullptr, ("WebAuthNAuthenticatorGetAssertion failed: " + HResultToString(hr)).c_str());
    return nullptr;
  }

  if (assertion) WebAuthNFreeAssertion(assertion);
  napi_value v;
  napi_get_boolean(env, true, &v);
  return v;
}

napi_value PromiseFromWork(napi_env env, AsyncContext* ctx, const char* name) {
  napi_value promise;
  napi_create_promise(env, &ctx->deferred, &promise);

  napi_value resource_name;
  napi_create_string_utf8(env, name, NAPI_AUTO_LENGTH, &resource_name);
  napi_create_async_work(env, nullptr, resource_name, Execute, Complete, ctx, &ctx->work);
  napi_queue_async_work(env, ctx->work);
  return promise;
}

napi_value IsAvailable(napi_env env, napi_callback_info info) {
  (void)info;
  auto* ctx = new AsyncContext();
  ctx->env = env;
  ctx->do_is_available = true;
  return PromiseFromWork(env, ctx, "netcatty_webauthn_isAvailable");
}

napi_value CreateCredential(napi_env env, napi_callback_info info) {
  size_t argc = 4;
  napi_value argv[4];
  napi_get_cb_info(env, info, &argc, argv, nullptr, nullptr);

  if (argc < 3) {
    napi_throw_type_error(env, nullptr, "createCredential(rpId, rpName, userName, hwndBuffer?)");
    return nullptr;
  }

  std::string rpId, rpName, userName;
  if (!GetStringArg(env, argv[0], rpId) || !GetStringArg(env, argv[1], rpName) || !GetStringArg(env, argv[2], userName)) {
    napi_throw_type_error(env, nullptr, "createCredential expects strings");
    return nullptr;
  }

  HWND hwnd = nullptr;
  if (argc >= 4) {
    bool isBuf = false;
    napi_is_buffer(env, argv[3], &isBuf);
    if (isBuf) {
      void* data = nullptr;
      size_t len = 0;
      napi_get_buffer_info(env, argv[3], &data, &len);
      hwnd = ReadHwndFromBuffer(data, len);
    }
  }

  auto* ctx = new AsyncContext();
  ctx->env = env;
  ctx->do_make_credential = true;
  ctx->rp_id = Utf8ToWide(rpId);
  ctx->rp_name = Utf8ToWide(rpName);
  ctx->user_name = Utf8ToWide(userName);
  ctx->parent_hwnd = hwnd;
  return PromiseFromWork(env, ctx, "netcatty_webauthn_createCredential");
}

napi_value GetAssertion(napi_env env, napi_callback_info info) {
  size_t argc = 3;
  napi_value argv[3];
  napi_get_cb_info(env, info, &argc, argv, nullptr, nullptr);

  if (argc < 2) {
    napi_throw_type_error(env, nullptr, "getAssertion(rpId, credentialIdBuffer, hwndBuffer?)");
    return nullptr;
  }

  std::string rpId;
  if (!GetStringArg(env, argv[0], rpId)) {
    napi_throw_type_error(env, nullptr, "rpId must be a string");
    return nullptr;
  }

  std::vector<uint8_t> credId;
  if (!GetBufferArg(env, argv[1], credId)) {
    napi_throw_type_error(env, nullptr, "credentialId must be a Buffer");
    return nullptr;
  }

  HWND hwnd = nullptr;
  if (argc >= 3) {
    bool isBuf = false;
    napi_is_buffer(env, argv[2], &isBuf);
    if (isBuf) {
      void* data = nullptr;
      size_t len = 0;
      napi_get_buffer_info(env, argv[2], &data, &len);
      hwnd = ReadHwndFromBuffer(data, len);
    }
  }

  auto* ctx = new AsyncContext();
  ctx->env = env;
  ctx->do_get_assertion = true;
  ctx->rp_id = Utf8ToWide(rpId);
  ctx->credential_id = std::move(credId);
  ctx->parent_hwnd = hwnd;
  return PromiseFromWork(env, ctx, "netcatty_webauthn_getAssertion");
}

napi_value Init(napi_env env, napi_value exports) {
  napi_value fn;
  if (napi_create_function(env, "isAvailable", NAPI_AUTO_LENGTH, IsAvailable, nullptr, &fn) == napi_ok) {
    napi_set_named_property(env, exports, "isAvailable", fn);
  }
  if (napi_create_function(env, "createCredential", NAPI_AUTO_LENGTH, CreateCredential, nullptr, &fn) == napi_ok) {
    napi_set_named_property(env, exports, "createCredential", fn);
  }
  if (napi_create_function(env, "getAssertion", NAPI_AUTO_LENGTH, GetAssertion, nullptr, &fn) == napi_ok) {
    napi_set_named_property(env, exports, "getAssertion", fn);
  }
  if (napi_create_function(env, "isAvailableSync", NAPI_AUTO_LENGTH, IsAvailableSync, nullptr, &fn) == napi_ok) {
    napi_set_named_property(env, exports, "isAvailableSync", fn);
  }
  if (napi_create_function(env, "createCredentialSync", NAPI_AUTO_LENGTH, CreateCredentialSync, nullptr, &fn) == napi_ok) {
    napi_set_named_property(env, exports, "createCredentialSync", fn);
  }
  if (napi_create_function(env, "getAssertionSync", NAPI_AUTO_LENGTH, GetAssertionSync, nullptr, &fn) == napi_ok) {
    napi_set_named_property(env, exports, "getAssertionSync", fn);
  }
  return exports;
}

}  // namespace

NAPI_MODULE(NODE_GYP_MODULE_NAME, Init)
