// Package locales holds Netcatty's ARB/ICU message catalogs and the accessors
// mygo generate writes from them. The accessors are generated code: edit the
// JSON catalogs, then run `go tool mygo generate`.
package locales

// Tags returns the languages the app ships, source language first, as the
// embedded catalogs declare them. The shell and the language menu offer them
// in this order.
func Tags() []string { return Catalog.Languages() }
