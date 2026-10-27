//go:build selftest

package app

// Ticket 35 self-test. Press F11, then 3, then 5. The check proves the
// protocol-selection contract end to end without an sshd:
//
//   - browsing and both transfer directions run against the in-process
//     SCP-only fixture (shell listing, scp -f, scp -t);
//   - the probe rejects a server without scp, and single-channel transports
//     refuse SFTP or its fallback;
//   - sudo SFTP performs the password-on-stdin marker handshake and fails
//     closed for a wrong password or a missing sftp-server;
//   - explicit SFTP never falls back, auto may, and sudo never downgrades.
//
// Every step is synchronous or checks a real condition; there is no sleep.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-35.json.

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/scptest/scpfixture"
	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/selftest"
)

const selftest35Ticket = 35

func (t *selftest35) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest35Ticket,
		Name:   "SCP, sudo, and protocol selection",
		Steps: []selftest.Step{
			{Name: "scp-browses-and-transfers", Run: t.scpStep},
			{Name: "probe-and-single-channel-refusal", Run: t.refusalStep},
			{Name: "sudo-password-handshake", Run: t.sudoStep},
			{Name: "explicit-versus-auto-selection", Run: t.selectionStep},
		},
	}
}

type selftest35 struct{}

// scpStep browses and transfers through the SCP-only fixture.
func (t *selftest35) scpStep(*ui.Context) selftest.Outcome {
	ctx := context.Background()
	fixture := scpfixture.New()
	fixture.WriteFile("/home/tester/notes.txt", "hello scp", 0o640)
	fixture.MkdirAll("/home/tester/docs")
	fixture.Symlink("/home/tester/docs-link", "docs")

	files, err := adaptersftp.NewSCP(fixture, nil, adaptersftp.WithSCPBacking("selftest-host"))
	if err != nil {
		return selftest.Failed(fmt.Errorf("create the SCP filesystem: %v", err))
	}
	defer files.Close()

	if err := adaptersftp.ProbeSCP(ctx, fixture); err != nil {
		return selftest.Failed(fmt.Errorf("probe the SCP-only server: %v", err))
	}
	root, err := files.Root(ctx)
	if err != nil || root != "/home/tester" {
		return selftest.Failed(fmt.Errorf("SCP root = %q (%v)", root, err))
	}
	entries, err := files.List(ctx, root)
	if err != nil {
		return selftest.Failed(fmt.Errorf("list over SCP: %v", err))
	}
	seen := map[string]domainsftp.Entry{}
	for _, entry := range entries {
		seen[entry.Name] = entry
	}
	if seen["notes.txt"].Kind != domainsftp.KindFile || seen["notes.txt"].Size != int64(len("hello scp")) {
		return selftest.Failed(fmt.Errorf("the SCP listing lost notes.txt: %+v", seen))
	}
	if seen["docs"].Kind != domainsftp.KindDir {
		return selftest.Failed(fmt.Errorf("the SCP listing lost the directory: %+v", seen["docs"]))
	}
	if link, ok := seen["docs-link"]; !ok || !link.Symlink || link.Kind != domainsftp.KindDir {
		return selftest.Failed(fmt.Errorf("the SCP listing did not resolve the directory link: %+v", link))
	}

	reader, err := files.Open(ctx, "/home/tester/notes.txt")
	if err != nil {
		return selftest.Failed(fmt.Errorf("open over scp -f: %v", err))
	}
	downloaded, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil || string(downloaded) != "hello scp" {
		return selftest.Failed(fmt.Errorf("downloaded %q (%v)", downloaded, readErr))
	}

	writer, err := files.CreateSized(ctx, "/home/tester/upload.bin", 5)
	if err != nil {
		return selftest.Failed(fmt.Errorf("open an upload over scp -t: %v", err))
	}
	if _, err := io.WriteString(writer, "12345"); err != nil {
		return selftest.Failed(fmt.Errorf("write the upload: %v", err))
	}
	if err := writer.Close(); err != nil {
		return selftest.Failed(fmt.Errorf("finish the upload: %v", err))
	}
	if got, ok := fixture.ReadFile("/home/tester/upload.bin"); !ok || got != "12345" {
		return selftest.Failed(fmt.Errorf("the uploaded file = %q, exists = %v", got, ok))
	}
	return selftest.Passed(
		"listed files, directories, and a resolved link; downloaded with scp -f and uploaded with scp -t",
		"adapters/sftp.SCP -> adapters/scptest/scpfixture (SCP-only server)")
}

// refusalStep proves the probe rejects a missing scp and single-channel hosts
// refuse SFTP without opening a channel.
func (t *selftest35) refusalStep(*ui.Context) selftest.Outcome {
	ctx := context.Background()
	missing := scpfixture.New(scpfixture.WithoutSCP())
	if err := adaptersftp.ProbeSCP(ctx, missing); !errors.Is(err, adaptersftp.ErrSCPMissing) {
		return selftest.Failed(fmt.Errorf("a server without scp probed as usable: %v", err))
	}

	shared := &selftest35Session{singleChannel: true, reused: true}
	connector := &selftest35Connector{session: shared}
	if _, _, err := apptransfer.SelectRemote(ctx, connector, apptransfer.RemoteOptions{
		HostID: "selftest", Protocol: apptransfer.ProtocolAuto,
	}); !errors.Is(err, apptransfer.ErrSingleChannel) {
		return selftest.Failed(fmt.Errorf("a reused single-channel transport error = %v", err))
	}
	if shared.sftpCalls != 0 || shared.scpCalls != 0 {
		return selftest.Failed(fmt.Errorf("a single-channel channel was opened (sftp=%d scp=%d)",
			shared.sftpCalls, shared.scpCalls))
	}

	fallback := &selftest35Session{singleChannel: true, sftpErr: errors.New("subsystem unavailable")}
	if _, _, err := apptransfer.SelectRemote(ctx, &selftest35Connector{session: fallback},
		apptransfer.RemoteOptions{HostID: "selftest", Protocol: apptransfer.ProtocolAuto}); !errors.Is(err, apptransfer.ErrSingleChannelFallback) {
		return selftest.Failed(fmt.Errorf("single-channel fallback error = %v", err))
	}
	if fallback.scpCalls != 0 {
		return selftest.Failed(fmt.Errorf("a single-channel host opened the SCP fallback"))
	}
	return selftest.Passed(
		"the probe rejected a missing scp; reused single-channel SFTP, SCP, and auto fallback were refused before any channel opened",
		"adapters/sftp.ProbeSCP + application/transfer.SelectRemote (single-channel rules)")
}

// sudoStep proves the password-on-stdin marker handshake and its typed
// failures.
func (t *selftest35) sudoStep(*ui.Context) selftest.Outcome {
	ctx := context.Background()
	fixture := scpfixture.New(scpfixture.WithSudoPassword("s3cr3t"))
	stream, err := adaptersftp.OpenSudoSFTP(ctx, fixture, "s3cr3t")
	if err != nil {
		return selftest.Failed(fmt.Errorf("sudo handshake: %v", err))
	}
	_ = stream.Close()

	wantCommand := "sudo -S -p 'SUDOPASSWORD:' sh -c 'printf SFTPREADY; exec /usr/lib/openssh/sftp-server -e'"
	foundCommand := ""
	for _, command := range fixture.Commands() {
		if len(command) > len("sudo -S") && command[:len("sudo -S")] == "sudo -S" {
			foundCommand = command
		}
	}
	if foundCommand != wantCommand {
		return selftest.Failed(fmt.Errorf("sudo command = %q", foundCommand))
	}

	if _, err := adaptersftp.OpenSudoSFTP(ctx, fixture, "wrong"); !errors.Is(err, adaptersftp.ErrSudoDenied) {
		return selftest.Failed(fmt.Errorf("wrong password error = %v", err))
	}
	noServer := scpfixture.New()
	if _, err := adaptersftp.OpenSudoSFTP(ctx, noServer, "s3cr3t"); !errors.Is(err, adaptersftp.ErrSudoServerMissing) {
		return selftest.Failed(fmt.Errorf("missing sftp-server error = %v", err))
	}
	return selftest.Passed(
		"the prompt and SFTPREADY marker carried the password on stdin; a wrong password and an absent sftp-server failed closed after the standard-path fallback",
		"adapters/sftp.OpenSudoSFTP -> adapters/scptest/scpfixture (sudo handshake)")
}

// selectionStep proves explicit SFTP does not fall back, auto may, and sudo
// never downgrades.
func (t *selftest35) selectionStep(*ui.Context) selftest.Outcome {
	ctx := context.Background()
	local, err := adaptersftp.NewLocal().OpenLocal(ctx)
	if err != nil {
		return selftest.Failed(fmt.Errorf("open a local filesystem: %v", err))
	}
	defer local.Close()

	subsystemFailure := errors.New("subsystem unavailable")
	explicit := &selftest35Session{sftpErr: subsystemFailure, sftpFiles: local, scpFiles: local}
	if _, _, err := apptransfer.SelectRemote(ctx, &selftest35Connector{session: explicit},
		apptransfer.RemoteOptions{HostID: "selftest", Protocol: apptransfer.ProtocolSFTP}); !errors.Is(err, subsystemFailure) {
		return selftest.Failed(fmt.Errorf("explicit SFTP error = %v", err))
	}
	if explicit.scpCalls != 0 {
		return selftest.Failed(fmt.Errorf("explicit SFTP fell back to SCP"))
	}

	auto := &selftest35Session{sftpErr: subsystemFailure, scpFiles: local}
	_, protocol, err := apptransfer.SelectRemote(ctx, &selftest35Connector{session: auto},
		apptransfer.RemoteOptions{HostID: "selftest", Protocol: apptransfer.ProtocolAuto})
	if err != nil || protocol != apptransfer.ProtocolSCP || auto.scpCalls != 1 {
		return selftest.Failed(fmt.Errorf("auto fallback = %q, scp=%d, err=%v", protocol, auto.scpCalls, err))
	}

	sudoFailure := errors.New("sudo refused")
	sudo := &selftest35Session{sudoErr: sudoFailure, sftpFiles: local, scpFiles: local, sudoFiles: local}
	if _, _, err := apptransfer.SelectRemote(ctx, &selftest35Connector{session: sudo},
		apptransfer.RemoteOptions{HostID: "selftest", Protocol: apptransfer.ProtocolAuto,
			Sudo: true, SudoPassword: "s3cr3t"}); !errors.Is(err, sudoFailure) {
		return selftest.Failed(fmt.Errorf("sudo failure error = %v", err))
	}
	if sudo.sftpCalls != 0 || sudo.scpCalls != 0 {
		return selftest.Failed(fmt.Errorf("sudo fell back (sftp=%d scp=%d)", sudo.sftpCalls, sudo.scpCalls))
	}

	connector := &selftest35Connector{session: &selftest35Session{}}
	if _, _, err := apptransfer.SelectRemote(ctx, connector, apptransfer.RemoteOptions{
		HostID: "selftest", Protocol: apptransfer.ProtocolSCP, Sudo: true,
	}); !errors.Is(err, apptransfer.ErrSudoWithSCP) {
		return selftest.Failed(fmt.Errorf("sudo with SCP error = %v", err))
	}
	if connector.connected {
		return selftest.Failed(fmt.Errorf("a contradictory sudo+SCP Host was connected before validation"))
	}
	return selftest.Passed(
		"explicit SFTP surfaced its failure, auto fell back once, and sudo opened neither unprivileged protocol",
		"application/transfer.SelectRemote (explicit, auto, and sudo rules)")
}

// selftest35Session is a transport fake for the selection rules.
type selftest35Session struct {
	singleChannel bool
	reused        bool
	sftpErr       error
	scpErr        error
	sudoErr       error
	sftpFiles     apptransfer.FileSystem
	scpFiles      apptransfer.FileSystem
	sudoFiles     apptransfer.FileSystem

	sftpCalls int
	scpCalls  int
	sudoCalls int
}

func (s *selftest35Session) SingleChannel() bool { return s.singleChannel }
func (s *selftest35Session) Reused() bool        { return s.reused }

func (s *selftest35Session) OpenSFTP(context.Context) (apptransfer.FileSystem, error) {
	s.sftpCalls++
	return s.sftpFiles, s.sftpErr
}

func (s *selftest35Session) OpenSCP(context.Context) (apptransfer.FileSystem, error) {
	s.scpCalls++
	return s.scpFiles, s.scpErr
}

func (s *selftest35Session) OpenSudoSFTP(context.Context, string) (apptransfer.FileSystem, error) {
	s.sudoCalls++
	return s.sudoFiles, s.sudoErr
}

func (s *selftest35Session) Discard() {}

// selftest35Connector returns one fixed session.
type selftest35Connector struct {
	session   *selftest35Session
	connected bool
}

func (c *selftest35Connector) ConnectRemote(_ context.Context, _ model.HostID) (apptransfer.RemoteSession, error) {
	c.connected = true
	return c.session, nil
}
