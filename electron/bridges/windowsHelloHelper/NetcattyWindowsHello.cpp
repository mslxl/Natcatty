#include <windows.h>
#include <inspectable.h>
#include <roapi.h>
#include <winstring.h>

#include <iostream>
#include <string>

// This helper is intentionally small: Netcatty's Electron main process owns
// policy; the helper only invokes the desktop HWND-bound Windows Hello API.

int wmain(int argc, wchar_t* argv[]) {
  if (argc < 2) {
    std::wcout << L"{\"ok\":false,\"error\":\"missing-command\"}\n";
    return 1;
  }

  const std::wstring command = argv[1];
  if (command == L"status") {
    // Placeholder implementation for the packaging batch. The bridge batch
    // replaces this with the WinRT UserConsentVerifier implementation.
    std::wcout << L"{\"supported\":true,\"available\":false,\"reason\":\"helper-not-built\"}\n";
    return 0;
  }

  if (command == L"verify") {
    std::wcout << L"{\"ok\":false,\"error\":\"unavailable\"}\n";
    return 0;
  }

  std::wcout << L"{\"ok\":false,\"error\":\"unknown-command\"}\n";
  return 1;
}
