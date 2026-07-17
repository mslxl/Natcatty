# Hello Netcatty

This package is a compile-time example for the internal plugin API. PR 1 does
not load or execute it inside Netcatty; the isolated host runtime arrives in PR
2 of the plugin platform series.

From the repository root:

```bash
npm run build --workspace @netcatty/example-hello-plugin
npm exec --workspace @netcatty/plugin-cli -- netcatty-plugin validate examples/plugins/hello-netcatty
npm exec --workspace @netcatty/plugin-cli -- netcatty-plugin compatibility examples/plugins/hello-netcatty --netcatty 0.0.0
```
