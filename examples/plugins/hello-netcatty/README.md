# Hello Netcatty

This package is the runnable internal example for the plugin API. With
`NETCATTY_PLUGIN_DEV=1`, package and install it to exercise phase-4 native
settings, command-palette contribution, lazy command activation, localization,
and the runtime SDK.

From the repository root:

```bash
npm run build:plugin-packages
npm exec -- netcatty-plugin validate examples/plugins/hello-netcatty
npm exec -- netcatty-plugin compatibility examples/plugins/hello-netcatty --netcatty 0.0.0
npm exec -- netcatty-plugin pack examples/plugins/hello-netcatty --out /tmp/hello-netcatty.ncpkg
```

After installation, change **Greeting** under **Settings → Plugins**, then run
**Examples: Say Hello** from the command palette. The setting is read through
the host settings broker and the command handler is registered inside the
sandboxed plugin runtime.
