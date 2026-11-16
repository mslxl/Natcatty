package transfer

import (
	"context"
	"errors"
	"testing"
	"time"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
)

// fakeRemoteSession records the selector's decisions.
type fakeRemoteSession struct {
	singleChannel bool
	reused        bool

	sftpFiles FileSystem
	sftpErr   error
	scpFiles  FileSystem
	scpErr    error
	sudoFiles FileSystem
	sudoErr   error

	openSFTP     int
	openSCP      int
	openSudo     int
	discarded    int
	scpDeadline  time.Duration
	sudoPassword string
}

func (s *fakeRemoteSession) SingleChannel() bool { return s.singleChannel }
func (s *fakeRemoteSession) Reused() bool        { return s.reused }

func (s *fakeRemoteSession) OpenSFTP(context.Context) (FileSystem, error) {
	s.openSFTP++
	return s.sftpFiles, s.sftpErr
}

func (s *fakeRemoteSession) OpenSCP(ctx context.Context) (FileSystem, error) {
	s.openSCP++
	if deadline, ok := ctx.Deadline(); ok {
		s.scpDeadline = time.Until(deadline)
	}
	return s.scpFiles, s.scpErr
}

func (s *fakeRemoteSession) OpenSudoSFTP(_ context.Context, password string) (FileSystem, error) {
	s.openSudo++
	s.sudoPassword = password
	return s.sudoFiles, s.sudoErr
}

func (s *fakeRemoteSession) Discard() { s.discarded++ }

// fakeDedicatedSession is a single-channel session with the companion-dial
// capability. The embedded session is the borrowed transport; dedicated is
// what DialDedicated hands back.
type fakeDedicatedSession struct {
	*fakeRemoteSession

	accepted     []domainconn.Key
	lastAccepted []domainconn.Key
	dedicated    RemoteSession
	dialErr      error
	dialErrOnce  []error
	dials        int
}

func (s *fakeDedicatedSession) AcceptedHostIdentities() []domainconn.Key {
	return append([]domainconn.Key(nil), s.accepted...)
}

func (s *fakeDedicatedSession) DialDedicated(_ context.Context, accepted []domainconn.Key) (RemoteSession, error) {
	s.dials++
	s.lastAccepted = append([]domainconn.Key(nil), accepted...)
	if len(s.dialErrOnce) > 0 {
		err := s.dialErrOnce[0]
		s.dialErrOnce = s.dialErrOnce[1:]
		return nil, err
	}
	if s.dialErr != nil {
		return nil, s.dialErr
	}
	return s.dedicated, nil
}

type fakeRemoteConnector struct {
	session RemoteSession
	err     error
	hostID  model.HostID
}

func (c *fakeRemoteConnector) ConnectRemote(_ context.Context, hostID model.HostID) (RemoteSession, error) {
	c.hostID = hostID
	if c.err != nil {
		return nil, c.err
	}
	return c.session, nil
}

func selectWith(t *testing.T, session RemoteSession, options RemoteOptions) (FileSystem, FileProtocol, error) {
	t.Helper()
	connector := &fakeRemoteConnector{session: session}
	if options.HostID == "" {
		options.HostID = "host-1"
	}
	return SelectRemote(context.Background(), connector, options)
}

func TestExplicitSCPUsesOnlySCPAndBoundsTheProbe(t *testing.T) {
	remote := newFakeFS("remote")
	session := &fakeRemoteSession{scpFiles: remote}
	files, protocol, err := selectWith(t, session, RemoteOptions{Protocol: ProtocolSCP})
	if err != nil {
		t.Fatalf("SelectRemote: %v", err)
	}
	if files != remote || protocol != ProtocolSCP {
		t.Fatalf("files = %v, protocol = %q", files, protocol)
	}
	if session.openSFTP != 0 {
		t.Fatal("explicit SCP tried SFTP")
	}
	if session.openSCP != 1 {
		t.Fatalf("OpenSCP calls = %d", session.openSCP)
	}
	if session.scpDeadline < 14*time.Second || session.scpDeadline > SCPProbeTimeout+time.Second {
		t.Fatalf("probe deadline = %v, want about %v", session.scpDeadline, SCPProbeTimeout)
	}
	if session.discarded != 0 {
		t.Fatal("a successful open discarded the session")
	}
}

func TestExplicitSFTPDoesNotFallBack(t *testing.T) {
	subsystemError := errors.New("subsystem unavailable")
	session := &fakeRemoteSession{
		sftpErr:  subsystemError,
		scpFiles: newFakeFS("remote"),
	}
	_, _, err := selectWith(t, session, RemoteOptions{Protocol: ProtocolSFTP})
	if !errors.Is(err, subsystemError) {
		t.Fatalf("error = %v, want the SFTP failure", err)
	}
	if session.openSCP != 0 {
		t.Fatal("explicit SFTP fell back to SCP")
	}
	if session.discarded != 1 {
		t.Fatalf("discarded = %d", session.discarded)
	}
}

func TestAutoFallsBackToSCPWhenSFTPIsUnavailable(t *testing.T) {
	remote := newFakeFS("remote")
	session := &fakeRemoteSession{
		sftpErr:  errors.New("subsystem unavailable"),
		scpFiles: remote,
	}
	files, protocol, err := selectWith(t, session, RemoteOptions{Protocol: ProtocolAuto})
	if err != nil {
		t.Fatalf("SelectRemote: %v", err)
	}
	if files != remote || protocol != ProtocolSCP {
		t.Fatalf("files = %v, protocol = %q", files, protocol)
	}
	if session.openSFTP != 1 || session.openSCP != 1 {
		t.Fatalf("openSFTP = %d, openSCP = %d", session.openSFTP, session.openSCP)
	}
	if session.discarded != 0 {
		t.Fatal("a successful fallback discarded the shared transport")
	}
}

func TestAutoRefusesSCPFallbackOnASingleChannelHost(t *testing.T) {
	session := &fakeRemoteSession{
		singleChannel: true,
		sftpErr:       errors.New("subsystem unavailable"),
		scpFiles:      newFakeFS("remote"),
	}
	_, _, err := selectWith(t, session, RemoteOptions{Protocol: ProtocolAuto})
	if !errors.Is(err, ErrSingleChannelFallback) {
		t.Fatalf("error = %v, want ErrSingleChannelFallback", err)
	}
	if session.openSCP != 0 {
		t.Fatal("a single-channel host opened the SCP fallback")
	}
	if session.discarded != 1 {
		t.Fatalf("discarded = %d", session.discarded)
	}
}

// A session that cannot dial independently is still refused: the selector has
// no safe way to open a second channel.
func TestReusedSingleChannelTransportWithoutCompanionDialIsRefused(t *testing.T) {
	for _, protocol := range []FileProtocol{ProtocolAuto, ProtocolSFTP, ProtocolSCP} {
		session := &fakeRemoteSession{
			singleChannel: true,
			reused:        true,
			sftpFiles:     newFakeFS("remote"),
			scpFiles:      newFakeFS("remote"),
		}
		_, _, err := selectWith(t, session, RemoteOptions{Protocol: protocol})
		if !errors.Is(err, ErrSingleChannel) {
			t.Errorf("%s error = %v, want ErrSingleChannel", protocol, err)
		}
		if session.openSFTP != 0 || session.openSCP != 0 {
			t.Errorf("%s opened a channel on a reused single-channel transport", protocol)
		}
		if session.discarded != 1 {
			t.Errorf("%s discarded = %d", protocol, session.discarded)
		}
	}
}

// TestReusedSingleChannelTransportOpensACompanionDial is the core port: a
// single-channel peer that already serves a terminal gets an independent
// Connection with the same profile, and the borrowed lease is returned before
// the dial so a failure cannot take the terminal down.
func TestReusedSingleChannelTransportOpensACompanionDial(t *testing.T) {
	accepted := []domainconn.Key{{
		Hostname: "gateway.example", Port: 22, KeyType: "ssh-ed25519",
		Fingerprint: "SHA256:accepted",
	}}
	cases := []struct {
		protocol FileProtocol
		want     FileProtocol
		prepare  func(*fakeRemoteSession) FileSystem
	}{
		{ProtocolSFTP, ProtocolSFTP, func(dedicated *fakeRemoteSession) FileSystem {
			files := newFakeFS("remote")
			dedicated.sftpFiles = files
			return files
		}},
		{ProtocolSCP, ProtocolSCP, func(dedicated *fakeRemoteSession) FileSystem {
			files := newFakeFS("remote")
			dedicated.scpFiles = files
			return files
		}},
		{ProtocolAuto, ProtocolSFTP, func(dedicated *fakeRemoteSession) FileSystem {
			files := newFakeFS("remote")
			dedicated.sftpFiles = files
			return files
		}},
	}
	for _, test := range cases {
		borrowed := &fakeRemoteSession{
			singleChannel: true,
			reused:        true,
			sftpFiles:     newFakeFS("borrowed"),
			scpFiles:      newFakeFS("borrowed"),
		}
		dedicated := &fakeRemoteSession{singleChannel: true}
		wantFiles := test.prepare(dedicated)
		session := &fakeDedicatedSession{
			fakeRemoteSession: borrowed,
			accepted:          accepted,
			dedicated:         dedicated,
		}

		files, protocol, err := selectWith(t, session, RemoteOptions{Protocol: test.protocol})
		if err != nil {
			t.Errorf("%s: SelectRemote: %v", test.protocol, err)
			continue
		}
		if files != wantFiles || protocol != test.want {
			t.Errorf("%s: files = %v, protocol = %q", test.protocol, files, protocol)
		}
		if session.dials != 1 {
			t.Errorf("%s: companion dials = %d, want 1", test.protocol, session.dials)
		}
		if len(session.lastAccepted) != 1 || session.lastAccepted[0].Fingerprint != "SHA256:accepted" {
			t.Errorf("%s: accepted identities = %+v", test.protocol, session.lastAccepted)
		}
		if borrowed.discarded != 1 {
			t.Errorf("%s: borrowed discard = %d, want 1", test.protocol, borrowed.discarded)
		}
		if borrowed.openSFTP != 0 || borrowed.openSCP != 0 {
			t.Errorf("%s: opened a channel on the borrowed transport", test.protocol)
		}
	}
}

// A read-only probe that must not consume a single-channel peer's only channel
// also uses the companion dial instead of refusing.
func TestRequireExtraChannelUsesTheCompanionDial(t *testing.T) {
	files := newFakeFS("remote")
	dedicated := &fakeRemoteSession{singleChannel: true, sftpFiles: files}
	session := &fakeDedicatedSession{
		fakeRemoteSession: &fakeRemoteSession{singleChannel: true},
		dedicated:         dedicated,
	}
	got, protocol, err := selectWith(t, session, RemoteOptions{
		Protocol: ProtocolSFTP, RequireExtraChannel: true,
	})
	if err != nil || got != files || protocol != ProtocolSFTP {
		t.Fatalf("SelectRemote = %v, %q, %v", got, protocol, err)
	}
	if session.dials != 1 {
		t.Fatalf("companion dials = %d, want 1", session.dials)
	}
	if session.fakeRemoteSession.discarded != 1 {
		t.Fatalf("borrowed discard = %d, want 1", session.fakeRemoteSession.discarded)
	}
	if session.fakeRemoteSession.openSFTP != 0 {
		t.Fatal("the borrowed single-channel transport opened a channel")
	}
}

// TestCompanionDialFailureFailsTheTransferWithAClearError proves a trust or
// lifecycle failure is attributed to the Transfer, not hidden or turned into
// a fresh login with different trust.
func TestCompanionDialFailureFailsTheTransferWithAClearError(t *testing.T) {
	cause := errors.New("host key is no longer trusted")
	borrowed := &fakeRemoteSession{singleChannel: true, reused: true}
	session := &fakeDedicatedSession{
		fakeRemoteSession: borrowed,
		accepted:          []domainconn.Key{{Hostname: "gateway.example", Fingerprint: "SHA256:old"}},
		dialErr:           cause,
	}
	files, protocol, err := selectWith(t, session, RemoteOptions{Protocol: ProtocolAuto})
	if files != nil || protocol != "" {
		t.Fatalf("a failed companion dial returned %v, %q", files, protocol)
	}
	if !errors.Is(err, ErrDedicatedDial) {
		t.Fatalf("error = %v, want ErrDedicatedDial", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want the trust failure", err)
	}
	if borrowed.discarded != 1 {
		t.Fatalf("borrowed discard = %d, want 1", borrowed.discarded)
	}
	if borrowed.openSFTP != 0 || borrowed.openSCP != 0 {
		t.Fatal("the borrowed terminal transport was used for the transfer")
	}
}

// TestCompanionDialTimeoutIsRetryable pins timeout recovery: a timed-out
// companion dial leaves nothing poisoned, so the next attempt dials again and
// succeeds.
func TestCompanionDialTimeoutIsRetryable(t *testing.T) {
	files := newFakeFS("remote")
	dedicated := &fakeRemoteSession{singleChannel: true, sftpFiles: files}
	borrowed := &fakeRemoteSession{singleChannel: true, reused: true}
	session := &fakeDedicatedSession{
		fakeRemoteSession: borrowed,
		dedicated:         dedicated,
		dialErrOnce:       []error{context.DeadlineExceeded},
	}
	_, _, err := selectWith(t, session, RemoteOptions{Protocol: ProtocolSFTP})
	if !errors.Is(err, ErrDedicatedDial) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first error = %v, want the timeout", err)
	}

	got, protocol, err := selectWith(t, session, RemoteOptions{Protocol: ProtocolSFTP})
	if err != nil || got != files || protocol != ProtocolSFTP {
		t.Fatalf("retry = %v, %q, %v", got, protocol, err)
	}
	if session.dials != 2 {
		t.Fatalf("companion dials = %d, want 2", session.dials)
	}
}

// The companion transport is still single-channel, so auto must not open an
// exec channel for the SCP fallback on it.
func TestCompanionDialKeepsTheSingleChannelFallbackRefusal(t *testing.T) {
	dedicated := &fakeRemoteSession{
		singleChannel: true,
		sftpErr:       errors.New("subsystem unavailable"),
		scpFiles:      newFakeFS("remote"),
	}
	session := &fakeDedicatedSession{
		fakeRemoteSession: &fakeRemoteSession{singleChannel: true, reused: true},
		dedicated:         dedicated,
	}
	_, _, err := selectWith(t, session, RemoteOptions{Protocol: ProtocolAuto})
	if !errors.Is(err, ErrSingleChannelFallback) {
		t.Fatalf("error = %v, want ErrSingleChannelFallback", err)
	}
	if dedicated.openSCP != 0 {
		t.Fatal("the companion transport opened the SCP fallback")
	}
	if dedicated.discarded != 1 {
		t.Fatalf("companion discard = %d, want 1", dedicated.discarded)
	}
}

func TestFreshSingleChannelHostMayUseSCPDirectlyAndSFTP(t *testing.T) {
	scp := newFakeFS("remote")
	session := &fakeRemoteSession{singleChannel: true, scpFiles: scp}
	files, protocol, err := selectWith(t, session, RemoteOptions{Protocol: ProtocolSCP})
	if err != nil || files != scp || protocol != ProtocolSCP {
		t.Fatalf("explicit SCP = %v, %q, %v", files, protocol, err)
	}

	sftp := newFakeFS("remote")
	session = &fakeRemoteSession{singleChannel: true, sftpFiles: sftp}
	files, protocol, err = selectWith(t, session, RemoteOptions{Protocol: ProtocolSFTP})
	if err != nil || files != sftp || protocol != ProtocolSFTP {
		t.Fatalf("explicit SFTP = %v, %q, %v", files, protocol, err)
	}
}

func TestSudoNeverFallsBackToUnprivilegedProtocols(t *testing.T) {
	sudoError := errors.New("sudo refused")
	session := &fakeRemoteSession{
		sftpFiles: newFakeFS("remote"),
		scpFiles:  newFakeFS("remote"),
		sudoErr:   sudoError,
	}
	_, _, err := selectWith(t, session, RemoteOptions{
		Protocol: ProtocolAuto, Sudo: true, SudoPassword: "s3cr3t",
	})
	if !errors.Is(err, sudoError) {
		t.Fatalf("error = %v, want the sudo failure", err)
	}
	if session.openSFTP != 0 || session.openSCP != 0 {
		t.Fatal("a failed sudo fell back to an unprivileged protocol")
	}
	if session.openSudo != 1 || session.sudoPassword != "s3cr3t" {
		t.Fatalf("openSudo = %d, password = %q", session.openSudo, session.sudoPassword)
	}
	if session.discarded != 1 {
		t.Fatalf("discarded = %d", session.discarded)
	}
}

func TestSudoWithSCPIsRejectedBeforeConnecting(t *testing.T) {
	connector := &fakeRemoteConnector{session: &fakeRemoteSession{}}
	_, _, err := SelectRemote(context.Background(), connector, RemoteOptions{
		HostID: "host-1", Protocol: ProtocolSCP, Sudo: true,
	})
	if !errors.Is(err, ErrSudoWithSCP) {
		t.Fatalf("error = %v, want ErrSudoWithSCP", err)
	}
	if connector.hostID != "" {
		t.Fatal("a contradictory Host was connected before validation")
	}
}

func TestAutoUsesSFTPWhenItWorks(t *testing.T) {
	remote := newFakeFS("remote")
	session := &fakeRemoteSession{sftpFiles: remote, scpFiles: newFakeFS("remote")}
	files, protocol, err := selectWith(t, session, RemoteOptions{Protocol: ProtocolAuto})
	if err != nil {
		t.Fatalf("SelectRemote: %v", err)
	}
	if files != remote || protocol != ProtocolSFTP {
		t.Fatalf("files = %v, protocol = %q", files, protocol)
	}
	if session.openSCP != 0 {
		t.Fatal("auto probed SCP after SFTP worked")
	}
}

func TestSCPProbeFailureIsReported(t *testing.T) {
	probeError := errors.New("scp is not installed")
	session := &fakeRemoteSession{scpErr: probeError}
	_, _, err := selectWith(t, session, RemoteOptions{Protocol: ProtocolSCP})
	if !errors.Is(err, probeError) {
		t.Fatalf("error = %v, want the probe failure", err)
	}
	if session.discarded != 1 {
		t.Fatalf("discarded = %d", session.discarded)
	}
}

func TestNormalizeFileProtocol(t *testing.T) {
	cases := map[string]FileProtocol{
		"":      ProtocolAuto,
		"auto":  ProtocolAuto,
		"sftp":  ProtocolSFTP,
		"scp":   ProtocolSCP,
		"bogus": ProtocolAuto,
	}
	for value, want := range cases {
		if got := NormalizeFileProtocol(value); got != want {
			t.Errorf("NormalizeFileProtocol(%q) = %q, want %q", value, got, want)
		}
	}
}

// The Host-Atuin detector needs its own channel: a Host whose connection
// offers only the terminal channel must be refused before either channel is
// opened, rather than sharing the terminal's.
func TestRequireExtraChannelRefusesASingleChannelHostBeforeAnyChannel(t *testing.T) {
	for _, protocol := range []FileProtocol{ProtocolAuto, ProtocolSFTP, ProtocolSCP} {
		session := &fakeRemoteSession{
			singleChannel: true,
			sftpFiles:     newFakeFS("remote"),
			scpFiles:      newFakeFS("remote"),
		}
		_, _, err := selectWith(t, session, RemoteOptions{
			Protocol: protocol, RequireExtraChannel: true,
		})
		if !errors.Is(err, ErrSingleChannel) {
			t.Errorf("%s error = %v, want ErrSingleChannel", protocol, err)
		}
		if session.openSFTP != 0 || session.openSCP != 0 {
			t.Errorf("%s opened a channel on a single-channel Host", protocol)
		}
		if session.discarded != 1 {
			t.Errorf("%s discarded = %d", protocol, session.discarded)
		}
	}
}

func TestRequireExtraChannelAllowsAMultiChannelHost(t *testing.T) {
	remote := newFakeFS("remote")
	session := &fakeRemoteSession{sftpFiles: remote}
	files, protocol, err := selectWith(t, session, RemoteOptions{
		Protocol: ProtocolSFTP, RequireExtraChannel: true,
	})
	if err != nil || files != remote || protocol != ProtocolSFTP {
		t.Fatalf("SelectRemote = %v, %q, %v", files, protocol, err)
	}
	if session.openSFTP != 1 || session.openSCP != 0 {
		t.Fatalf("openSFTP = %d, openSCP = %d", session.openSFTP, session.openSCP)
	}
}
