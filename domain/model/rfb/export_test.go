package rfb

import "crypto/x509"

// SetSystemRootsForTest replaces the trust-store loader for the duration of a
// test. It exists only in the test binary (Go compiles *_test.go files into the
// package under test), so production code has no way to substitute roots.
func SetSystemRootsForTest(pool *x509.CertPool) (restore func()) {
	old := systemRoots
	systemRoots = func() (*x509.CertPool, error) { return pool, nil }
	return func() { systemRoots = old }
}
