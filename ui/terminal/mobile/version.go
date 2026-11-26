package mobile

// Version is the library version the staged artifact must report. It is the
// ABI MyGo v0.10.0's Go bindings mirror, and it must agree with libVersion in
// ui/terminal/artifacts.json: a mismatched object fails to load rather than
// misreading a cell at run time.
const Version = "0.1.0-dev"
