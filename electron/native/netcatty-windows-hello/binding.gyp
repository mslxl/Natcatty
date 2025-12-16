{
  "targets": [
    {
      "target_name": "netcatty_windows_hello",
      "configurations": {
        "Debug": {
          "msbuild_toolset": "v143"
        },
        "Release": {
          "msbuild_toolset": "v143"
        }
      },
      "sources": [
        "src/addon.cc"
      ],
      "defines": [
        "NAPI_EXPERIMENTAL"
      ],
      "cflags_cc!": [
        "-fno-exceptions"
      ],
      "msvs_settings": {
        "VCCLCompilerTool": {
          "AdditionalOptions": [
            "/std:c++20",
            "/EHsc"
          ]
        }
      },
      "libraries": [
        "webauthn.lib",
        "bcrypt.lib",
        "crypt32.lib"
      ]
    }
  ]
}
