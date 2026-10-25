//go:build selftest

package app

// Ticket 42 self-test. Press F11, then 4, then 2. The check proves the
// FIDO/security-key path using the system OpenSSH agent contract and an
// in-process fake agent (there is no hardware key on this machine):
//
//   - the adapter lists the agent's identities, preserves the exact SK public
//     blob, and the application selects only the two SK algorithms;
//   - signing preserves the OpenSSH PROTOCOL.u2f framing: algorithm string, raw
//     signature, the user-presence flag, and the counter. x/crypto's own SK
//     verifier accepts the signature, which proves the application string
//     survived;
//   - a missing OpenSSH agent is a typed unsupported failure, not a raw
//     transport error;
//   - the real Dialer authenticates against an in-process SSH server with the
//     SK identity through the agent;
//   - the production 30-second device deadline returns a typed timeout instead
//     of hanging when the device never answers.
//
// Every wait observes a result with a hard deadline; there is no sleep. The
// deadline step intentionally waits the real 30 seconds.
//
// Result: %TEMP%\natcatty-selftest-42.json.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/egoist/mygo/ui"
	xssh "golang.org/x/crypto/ssh"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/sshtest/fakeagent"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/ui/selftest"
)

const (
	selftest42Ticket = 42
	// selftest42Wait bounds the in-process operations.
	selftest42Wait = 5 * time.Second
	// selftest42DeadlineSlack bounds the 30-second device deadline step.
	selftest42DeadlineSlack = 45 * time.Second
)

type selftest42 struct {
	// deadline state persists between frames. It is reset by the first step,
	// because the registry keeps one Check for the life of the process and its
	// steps run again for a repeated self-test.
	deadlineStarted   bool
	deadlineDone      chan error
	deadlineStartedAt time.Time
	deadlineHard      time.Time
}

func (t *selftest42) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest42Ticket,
		Name:   "FIDO security keys through the OpenSSH agent",
		Steps: []selftest.Step{
			{Name: "identities-and-missing-agent", Run: t.identitiesStep},
			{Name: "signature-framing-vectors", Run: t.framingStep},
			{Name: "session-authenticates-with-the-sk-key", Run: t.authStep},
			t.deadlineStep(),
		},
	}
}

func selftest42Service(agent *fakeagent.Agent) appconnection.SecurityKeyService {
	return appconnection.SecurityKeyService{
		Agent: sshadapter.NewSecurityKeyAgent(sshadapter.WithSecurityKeyAgentDial(agent.Dial)),
	}
}

// identitiesStep proves exact identity blobs, application filtering, and the
// typed unsupported failure when no OpenSSH agent is reachable.
func (t *selftest42) identitiesStep(*ui.Context) selftest.Outcome {
	// A repeated self-test reuses the registered steps; reset the deadline
	// step's per-run state before it runs again.
	t.deadlineStarted = false
	t.deadlineDone = nil

	hardware := fakeagent.Ed25519SK("selftest-sk")
	software := fakeagent.SoftwareEd25519("selftest-software")
	agent := fakeagent.New(hardware, software)
	keys, err := selftest42Service(agent).Identities(context.Background())
	if err != nil {
		return selftest.Failed(fmt.Errorf("list agent identities: %w", err))
	}
	if len(keys) != 1 {
		return selftest.Failed(fmt.Errorf("selected %d identities, want only the hardware one", len(keys)))
	}
	if keys[0].Algorithm != appconnection.SecurityKeyAlgorithmEd25519 ||
		keys[0].Comment != "selftest-sk" ||
		!bytes.Equal(keys[0].Blob, hardware.Blob) {
		return selftest.Failed(errors.New("the SK identity blob or comment changed"))
	}

	unavailable := appconnection.SecurityKeyService{Agent: sshadapter.NewSecurityKeyAgent(
		sshadapter.WithSecurityKeyAgentDial(func(context.Context) (net.Conn, error) {
			return nil, sshadapter.ErrAgentUnavailable
		}))}
	_, err = unavailable.Identities(context.Background())
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindUnsupported ||
		!errors.Is(err, appconnection.ErrSecurityKeyUnsupported) {
		return selftest.Failed(fmt.Errorf("missing agent error = %v, want a typed unsupported failure", err))
	}

	return selftest.Passed(
		"the agent's Ed25519-SK blob and comment survived unchanged, the software key was filtered, and a missing agent was typed unsupported",
		"adapters/ssh.AgentSecurityKeyAgent + application/connection.SelectSecurityKeys")
}

// framingStep signs with both SK algorithms and checks the exact wire framing
// and a cryptographic verification that depends on the application string and
// the user-presence flag.
func (t *selftest42) framingStep(*ui.Context) selftest.Outcome {
	cases := []struct {
		identity    fakeagent.Identity
		application string
	}{
		{fakeagent.Ed25519SKWithApplication("selftest-ed25519", "ssh:selftest"), "ssh:selftest"},
		{fakeagent.ECDSASK("selftest-ecdsa"), "ssh:"},
	}
	for _, tc := range cases {
		agent := fakeagent.New(tc.identity)
		service := selftest42Service(agent)
		keys, err := service.Identities(context.Background())
		if err != nil {
			return selftest.Failed(fmt.Errorf("%s: list identities: %w", tc.identity.Algorithm, err))
		}
		if len(keys) != 1 {
			return selftest.Failed(fmt.Errorf("%s: identities = %d, want 1", tc.identity.Algorithm, len(keys)))
		}
		if !bytes.Contains(keys[0].Blob, []byte(tc.application)) {
			return selftest.Failed(fmt.Errorf("%s: the public blob lost the application string", tc.identity.Algorithm))
		}

		challenge := []byte("selftest challenge")
		result, err := service.Sign(context.Background(), keys[0], challenge)
		if err != nil {
			return selftest.Failed(fmt.Errorf("%s: sign: %w", tc.identity.Algorithm, err))
		}
		var wire struct {
			Format string
			Blob   []byte
			Rest   []byte `ssh:"rest"`
		}
		if err := xssh.Unmarshal(result.Blob, &wire); err != nil {
			return selftest.Failed(fmt.Errorf("%s: signature framing: %w", tc.identity.Algorithm, err))
		}
		if wire.Format != tc.identity.Algorithm {
			return selftest.Failed(fmt.Errorf("%s: signature format = %q", tc.identity.Algorithm, wire.Format))
		}
		if want := []byte{0x01, 0, 0, 0, 7}; !bytes.Equal(wire.Rest, want) {
			return selftest.Failed(fmt.Errorf("%s: signature tail = %v, want %v", tc.identity.Algorithm, wire.Rest, want))
		}
		parsed, err := sshadapter.ParseSecurityKeySignature(result.Blob)
		if err != nil {
			return selftest.Failed(fmt.Errorf("%s: parse signature: %w", tc.identity.Algorithm, err))
		}
		if !bytes.Equal(xssh.Marshal(parsed), result.Blob) {
			return selftest.Failed(fmt.Errorf("%s: the signature does not round-trip", tc.identity.Algorithm))
		}
		public, err := xssh.ParsePublicKey(keys[0].Blob)
		if err != nil {
			return selftest.Failed(fmt.Errorf("%s: parse public key: %w", tc.identity.Algorithm, err))
		}
		if err := public.Verify(challenge, parsed); err != nil {
			return selftest.Failed(fmt.Errorf("%s: verify: %w", tc.identity.Algorithm, err))
		}

		requests := agent.SignRequests()
		if len(requests) != 1 {
			return selftest.Failed(fmt.Errorf("%s: sign requests = %d, want 1", tc.identity.Algorithm, len(requests)))
		}
		if !bytes.Equal(requests[0].KeyBlob, tc.identity.Blob) ||
			!bytes.Equal(requests[0].Data, challenge) || requests[0].Flags != 0 {
			return selftest.Failed(fmt.Errorf("%s: the sign request did not preserve the blob, challenge, and zero flags", tc.identity.Algorithm))
		}
	}

	return selftest.Passed(
		"Ed25519-SK and ECDSA-SK signatures kept the algorithm, raw signature, user-presence flag, and counter; both verified with the application string",
		"adapters/ssh.ParseSecurityKeySignature + PROTOCOL.u2f framing + in-process fake agent")
}

// authStep drives the real Dialer against an in-process SSH server that
// authorizes the fake agent's SK key.
func (t *selftest42) authStep(*ui.Context) selftest.Outcome {
	agent := fakeagent.Default()
	clientConn, serverConn := fakessh.Pipe()
	server, err := fakessh.Start(serverConn, fakessh.Options{PublicKeys: agent.PublicKeys()})
	if err != nil {
		_ = clientConn.Close()
		_ = serverConn.Close()
		return selftest.Failed(fmt.Errorf("start the in-process SSH server: %w", err))
	}
	defer func() {
		_ = server.Close()
		_ = clientConn.Close()
		_ = serverConn.Close()
	}()

	dialer := sshadapter.NewDialer(
		sshadapter.WithDialFunc(func(context.Context, string, string) (net.Conn, error) {
			return clientConn, nil
		}),
		sshadapter.WithAgentDial(agent.Dial),
		sshadapter.WithTimeouts(2*time.Second, selftest42Wait),
	)
	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice", Mode: appconnection.AuthModeAuto, UseAgent: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), selftest42Wait)
	defer cancel()
	client, err := dialer.Dial(ctx, appconnection.DialRequest{
		Hostname: "sk.selftest", Port: 22, Username: "alice", Auth: plan,
		VerifyHostKey: func(context.Context, domainconn.Key) error { return nil },
	})
	if err != nil {
		return selftest.Failed(fmt.Errorf("authenticate with the SK identity: %w", err))
	}
	defer client.Close()

	if server.PublicKeyAttempts() == 0 {
		return selftest.Failed(errors.New("the server saw no publickey attempt"))
	}
	if keyType, _ := server.LastPublicKey(); keyType != appconnection.SecurityKeyAlgorithmEd25519 {
		return selftest.Failed(fmt.Errorf("the server saw key type %q, want %q",
			keyType, appconnection.SecurityKeyAlgorithmEd25519))
	}
	requests := agent.SignRequests()
	if len(requests) != 1 {
		return selftest.Failed(fmt.Errorf("agent sign requests = %d, want 1", len(requests)))
	}
	if requests[0].Flags != 0 || len(requests[0].Data) == 0 {
		return selftest.Failed(errors.New("the agent signed with the wrong flags or an empty challenge"))
	}

	return selftest.Passed(
		"the real Dialer authenticated an in-process SSH server with the agent's Ed25519-SK key and zero sign-request flags",
		"adapters/ssh.Dialer + authMachine + adapters/sshtest/fakessh")
}

// deadlineStep runs the production 30-second device deadline for real. The
// signing call blocks in a goroutine; the step returns Pending until it ends.
func (t *selftest42) deadlineStep() selftest.Step {
	return selftest.Step{
		Name: "thirty-second-device-deadline",
		Run: func(*ui.Context) selftest.Outcome {
			if !t.deadlineStarted {
				t.deadlineStarted = true
				t.deadlineStartedAt = time.Now()
				t.deadlineHard = t.deadlineStartedAt.Add(selftest42DeadlineSlack)
				t.deadlineDone = make(chan error, 1)
				agent := fakeagent.NewWithOptions(
					fakeagent.WithIdentity(fakeagent.Ed25519SK("blocked")),
					fakeagent.WithBlockSign(),
				)
				service := selftest42Service(agent)
				keys, err := service.Identities(context.Background())
				if err != nil {
					return selftest.Failed(fmt.Errorf("list the blocking agent: %w", err))
				}
				done := t.deadlineDone
				go func() {
					_, err := service.Sign(context.Background(), keys[0], []byte("deadline challenge"))
					done <- err
				}()
				return selftest.Pending
			}
			select {
			case err := <-t.deadlineDone:
				elapsed := time.Since(t.deadlineStartedAt)
				var typed *appconnection.Error
				if !errors.As(err, &typed) || typed.Kind != appconnection.KindTimeout ||
					!errors.Is(err, appconnection.ErrSecurityKeyDeviceTimeout) {
					return selftest.Failed(fmt.Errorf("deadline error = %v, want a typed device timeout", err))
				}
				if elapsed < 29*time.Second {
					return selftest.Failed(fmt.Errorf("the deadline fired after %s, before 30 s", elapsed))
				}
				if elapsed > 40*time.Second {
					return selftest.Failed(fmt.Errorf("the deadline took %s, longer than 30 s plus slack", elapsed))
				}
				return selftest.Passed(
					fmt.Sprintf("the default 30 s device deadline returned ErrSecurityKeyDeviceTimeout after %.1f s", elapsed.Seconds()),
					"application/connection.SecurityKeyDeviceDeadline")
			default:
				if time.Now().After(t.deadlineHard) {
					return selftest.Failed(fmt.Errorf("timed out after %s waiting for the device deadline", selftest42DeadlineSlack))
				}
				return selftest.Pending
			}
		},
	}
}
