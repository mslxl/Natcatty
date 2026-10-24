package transfer

import (
	"context"
	"errors"
	"testing"
	"time"

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

type fakeRemoteConnector struct {
	session *fakeRemoteSession
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

func selectWith(t *testing.T, session *fakeRemoteSession, options RemoteOptions) (FileSystem, FileProtocol, error) {
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

func TestReusedSingleChannelTransportIsRefusedBeforeAnyChannel(t *testing.T) {
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
