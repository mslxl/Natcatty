package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
)

type promptLog struct {
	passphraseReqs []appconnection.PassphraseRequest
	kiReqs         []appconnection.KeyboardInteractiveRequest
}

type fakeAuthPrompter struct {
	mu         sync.Mutex
	passphrase func(appconnection.PassphraseRequest) (appconnection.PassphraseDecision, error)
	keyboard   func(appconnection.KeyboardInteractiveRequest) (appconnection.KeyboardInteractiveDecision, error)
	log        promptLog
}

func (f *fakeAuthPrompter) Passphrase(_ context.Context, req appconnection.PassphraseRequest) (appconnection.PassphraseDecision, error) {
	f.mu.Lock()
	f.log.passphraseReqs = append(f.log.passphraseReqs, req)
	f.mu.Unlock()
	if f.passphrase == nil {
		return appconnection.PassphraseDecision{Passphrase: "testkey"}, nil
	}
	return f.passphrase(req)
}

func (f *fakeAuthPrompter) KeyboardInteractive(_ context.Context, req appconnection.KeyboardInteractiveRequest) (appconnection.KeyboardInteractiveDecision, error) {
	f.mu.Lock()
	f.log.kiReqs = append(f.log.kiReqs, req)
	f.mu.Unlock()
	if f.keyboard == nil {
		return appconnection.KeyboardInteractiveDecision{Answers: []string{"123456"}}, nil
	}
	return f.keyboard(req)
}

func (f *fakeAuthPrompter) requests() promptLog {
	f.mu.Lock()
	defer f.mu.Unlock()
	return promptLog{
		passphraseReqs: append([]appconnection.PassphraseRequest(nil), f.log.passphraseReqs...),
		kiReqs:         append([]appconnection.KeyboardInteractiveRequest(nil), f.log.kiReqs...),
	}
}

func testSigner(t *testing.T) (xssh.Signer, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := xssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	block, err := xssh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return signer, string(pem.EncodeToMemory(block))
}

func testEncryptedSigner(t *testing.T, passphrase string) (xssh.Signer, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := xssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	block, err := xssh.MarshalPrivateKeyWithPassphrase(private, "test", []byte(passphrase))
	if err != nil {
		t.Fatalf("marshal encrypted key: %v", err)
	}
	return signer, string(pem.EncodeToMemory(block))
}

func dialWithPlan(t *testing.T, dialFunc DialFunc, plan appconnection.AuthPlan, prompter appconnection.AuthPrompter, opts ...Option) (appconnection.Client, error) {
	t.Helper()
	options := []Option{WithDialFunc(dialFunc), WithTimeouts(2*time.Second, 2*time.Second)}
	options = append(options, opts...)
	dialer := NewDialer(options...)
	return dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "alice",
		Auth:          plan,
		Prompter:      prompter,
		VerifyHostKey: acceptEveryKey,
	})
}

func TestDialAuthenticatesWithSelectedKey(t *testing.T) {
	userSigner, keyPEM := testSigner(t)
	server, dialFunc := startFixture(t, fakessh.Options{PublicKeys: []xssh.PublicKey{userSigner.PublicKey()}})

	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice",
		Mode:     appconnection.AuthModeKey,
		SelectedKey: &appconnection.KeyMaterial{
			ID: "key-1", Label: "work", PrivateKey: keyPEM,
		},
	})
	var succeeded string
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(2*time.Second, 2*time.Second))
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "alice", Auth: plan,
		VerifyHostKey: acceptEveryKey,
		OnAuth:        func(id string) { succeeded = id },
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if server.PublicKeyAttempts() == 0 {
		t.Fatal("the server saw no publickey attempt")
	}
	if succeeded != "publickey-user" {
		t.Errorf("OnAuth candidate = %q, want publickey-user", succeeded)
	}
	if server.PasswordAttempts() != 0 {
		t.Errorf("password attempts = %d in key-only mode, want 0", server.PasswordAttempts())
	}
}

func TestDialPromptsForAnEncryptedKeyPassphrase(t *testing.T) {
	_, keyPEM := testEncryptedSigner(t, "testkey")
	userSigner, err := xssh.ParsePrivateKeyWithPassphrase([]byte(keyPEM), []byte("testkey"))
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	_, dialFunc := startFixture(t, fakessh.Options{PublicKeys: []xssh.PublicKey{userSigner.PublicKey()}})
	prompter := &fakeAuthPrompter{}

	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice", Mode: appconnection.AuthModeKey,
		SelectedKey: &appconnection.KeyMaterial{ID: "key-1", Label: "work", PrivateKey: keyPEM},
	})
	client, err := dialWithPlan(t, dialFunc, plan, prompter)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	requests := prompter.requests()
	if len(requests.passphraseReqs) != 1 {
		t.Fatalf("passphrase prompts = %d, want 1", len(requests.passphraseReqs))
	}
	if requests.passphraseReqs[0].Invalid {
		t.Error("the first prompt was marked invalid")
	}
	if requests.passphraseReqs[0].KeyLabel != "work" {
		t.Errorf("prompt key label = %q, want work", requests.passphraseReqs[0].KeyLabel)
	}
}

func TestDialRepromptsAfterAStoredPassphraseFails(t *testing.T) {
	_, keyPEM := testEncryptedSigner(t, "testkey")
	userSigner, err := xssh.ParsePrivateKeyWithPassphrase([]byte(keyPEM), []byte("testkey"))
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	_, dialFunc := startFixture(t, fakessh.Options{PublicKeys: []xssh.PublicKey{userSigner.PublicKey()}})
	prompter := &fakeAuthPrompter{}

	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice", Mode: appconnection.AuthModeKey,
		SelectedKey: &appconnection.KeyMaterial{
			ID: "key-1", Label: "work", PrivateKey: keyPEM, Passphrase: "stale",
		},
	})
	client, err := dialWithPlan(t, dialFunc, plan, prompter)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	requests := prompter.requests()
	if len(requests.passphraseReqs) != 1 {
		t.Fatalf("passphrase prompts = %d, want 1", len(requests.passphraseReqs))
	}
	if !requests.passphraseReqs[0].Invalid {
		t.Error("the prompt after a failed stored passphrase was not marked invalid")
	}
}

func TestDialPassphraseCancellationIsTyped(t *testing.T) {
	_, keyPEM := testEncryptedSigner(t, "testkey")
	_, dialFunc := startFixture(t, fakessh.Options{})
	prompter := &fakeAuthPrompter{passphrase: func(appconnection.PassphraseRequest) (appconnection.PassphraseDecision, error) {
		return appconnection.PassphraseDecision{Cancelled: true}, nil
	}}

	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice", Mode: appconnection.AuthModeKey,
		SelectedKey: &appconnection.KeyMaterial{ID: "key-1", Label: "work", PrivateKey: keyPEM},
	})
	_, err := dialWithPlan(t, dialFunc, plan, prompter)
	assertKind(t, err, appconnection.KindCancelled)
	if !errors.Is(err, appconnection.ErrPromptCancelled) {
		t.Fatalf("error = %v, want ErrPromptCancelled", err)
	}
}

func TestDialAuthenticatesWithACertificate(t *testing.T) {
	_, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	caSigner, err := xssh.NewSignerFromKey(caPrivate)
	if err != nil {
		t.Fatalf("CA signer: %v", err)
	}
	_, userPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userSigner, err := xssh.NewSignerFromKey(userPrivate)
	if err != nil {
		t.Fatalf("user signer: %v", err)
	}
	certificate := &xssh.Certificate{
		Key:             userSigner.PublicKey(),
		CertType:        xssh.UserCert,
		KeyId:           "ticket-07",
		ValidPrincipals: []string{"alice"},
		ValidAfter:      uint64(time.Now().Add(-time.Minute).Unix()),
		ValidBefore:     uint64(time.Now().Add(time.Hour).Unix()),
	}
	if err := certificate.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatalf("sign certificate: %v", err)
	}
	userBlock, err := xssh.MarshalPrivateKey(userPrivate, "")
	if err != nil {
		t.Fatalf("marshal user key: %v", err)
	}

	server, dialFunc := startFixture(t, fakessh.Options{CertificateAuthority: caSigner.PublicKey()})
	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice", Mode: appconnection.AuthModeCertificate,
		SelectedKey: &appconnection.KeyMaterial{
			ID: "key-1", Label: "cert", PrivateKey: string(pem.EncodeToMemory(userBlock)),
			Certificate: string(xssh.MarshalAuthorizedKey(certificate)),
		},
	})
	client, err := dialWithPlan(t, dialFunc, plan, &fakeAuthPrompter{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	keyType, keyID := server.LastPublicKey()
	if !strings.Contains(keyType, "cert-v01@openssh.com") {
		t.Errorf("offered key type = %q, want a certificate", keyType)
	}
	if keyID != "ticket-07" {
		t.Errorf("certificate KeyId = %q, want ticket-07", keyID)
	}
}

func TestDialAuthenticatesWithTheSSHAgent(t *testing.T) {
	agentKey, agentPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate agent key: %v", err)
	}
	agentPublic, err := xssh.NewPublicKey(agentKey)
	if err != nil {
		t.Fatalf("agent public key: %v", err)
	}
	server, dialFunc := startFixture(t, fakessh.Options{PublicKeys: []xssh.PublicKey{agentPublic}})

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: agentPrivate, Comment: "agent-key"}); err != nil {
		t.Fatalf("add agent key: %v", err)
	}
	go func() { _ = agent.ServeAgent(keyring, serverConn) }()

	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice", Mode: appconnection.AuthModeAuto, UseAgent: true,
	})
	client, err := dialWithPlan(t, dialFunc, plan, &fakeAuthPrompter{}, WithAgentDial(func(context.Context) (net.Conn, error) {
		return clientConn, nil
	}))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if server.PublicKeyAttempts() == 0 {
		t.Fatal("the server saw no agent publickey attempt")
	}
}

func TestDialRunsOrderedKeyboardInteractiveRounds(t *testing.T) {
	_, dialFunc := startFixture(t, fakessh.Options{
		Password: "never-the-right-one",
		KeyboardInteractive: func(_ xssh.ConnMetadata, challenge xssh.KeyboardInteractiveChallenge) (*xssh.Permissions, error) {
			answers, err := challenge("login", "first factor", []string{"Password: "}, []bool{false})
			if err != nil {
				return nil, err
			}
			if len(answers) != 1 || answers[0] != "hunter2" {
				return nil, errors.New("fakessh: wrong first factor")
			}
			answers, err = challenge("verify", "second factor", []string{"Verification code: "}, []bool{false})
			if err != nil {
				return nil, err
			}
			if len(answers) != 1 || answers[0] != "123456" {
				return nil, errors.New("fakessh: wrong second factor")
			}
			return nil, nil
		},
	})
	prompter := &fakeAuthPrompter{keyboard: func(req appconnection.KeyboardInteractiveRequest) (appconnection.KeyboardInteractiveDecision, error) {
		if req.Name == "login" {
			return appconnection.KeyboardInteractiveDecision{Answers: []string{"hunter2"}}, nil
		}
		return appconnection.KeyboardInteractiveDecision{Answers: []string{"123456"}}, nil
	}}
	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice", Mode: appconnection.AuthModePassword,
	})
	client, err := dialWithPlan(t, dialFunc, plan, prompter)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	requests := prompter.requests()
	if len(requests.kiReqs) != 2 {
		t.Fatalf("keyboard-interactive rounds = %d, want 2", len(requests.kiReqs))
	}
	if requests.kiReqs[0].Name != "login" || requests.kiReqs[1].Name != "verify" {
		t.Errorf("round order = %q, %q", requests.kiReqs[0].Name, requests.kiReqs[1].Name)
	}
	if len(requests.kiReqs[0].Prompts) != 1 || requests.kiReqs[0].Prompts[0].Echo {
		t.Errorf("first round prompts = %+v, want one non-echo prompt", requests.kiReqs[0].Prompts)
	}
}

func TestDialCompletesPasswordThenKeyboardInteractivePartialSuccess(t *testing.T) {
	server, dialFunc := startFixture(t, fakessh.Options{
		Password:                        "hunter2",
		PasswordThenKeyboardInteractive: true,
		KeyboardInteractive: func(_ xssh.ConnMetadata, challenge xssh.KeyboardInteractiveChallenge) (*xssh.Permissions, error) {
			answers, err := challenge("verify", "second factor", []string{"One-time password: "}, []bool{false})
			if err != nil {
				return nil, err
			}
			if len(answers) != 1 || answers[0] != "654321" {
				return nil, errors.New("fakessh: wrong OTP")
			}
			return nil, nil
		},
	})
	prompter := &fakeAuthPrompter{keyboard: func(appconnection.KeyboardInteractiveRequest) (appconnection.KeyboardInteractiveDecision, error) {
		return appconnection.KeyboardInteractiveDecision{Answers: []string{"654321"}}, nil
	}}
	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice", Mode: appconnection.AuthModePassword, Password: "hunter2",
	})
	client, err := dialWithPlan(t, dialFunc, plan, prompter)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if server.PasswordAttempts() == 0 {
		t.Fatal("the password factor was not attempted")
	}
	rounds := server.KeyboardInteractiveRounds()
	if len(rounds) != 1 {
		t.Fatalf("keyboard-interactive rounds = %d, want 1", len(rounds))
	}
	if got := rounds[0].Answers; len(got) != 1 || got[0] != "654321" {
		t.Errorf("OTP answers = %v", got)
	}
}

func TestDialAutoFillsTheSavedPasswordIntoASinglePrompt(t *testing.T) {
	_, dialFunc := startFixture(t, fakessh.Options{
		Password: "not-the-saved-password",
		KeyboardInteractive: func(_ xssh.ConnMetadata, challenge xssh.KeyboardInteractiveChallenge) (*xssh.Permissions, error) {
			answers, err := challenge("", "", []string{"Password: "}, []bool{false})
			if err != nil {
				return nil, err
			}
			if len(answers) != 1 || answers[0] != "hunter2" {
				return nil, errors.New("fakessh: wrong KI password")
			}
			return nil, nil
		},
	})
	prompter := &fakeAuthPrompter{keyboard: func(appconnection.KeyboardInteractiveRequest) (appconnection.KeyboardInteractiveDecision, error) {
		t.Error("the saved password should have been auto-filled without a prompt")
		return appconnection.KeyboardInteractiveDecision{}, nil
	}}
	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice", Mode: appconnection.AuthModePassword, Password: "hunter2",
	})
	client, err := dialWithPlan(t, dialFunc, plan, prompter)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if got := len(prompter.requests().kiReqs); got != 0 {
		t.Errorf("keyboard-interactive prompts = %d, want 0", got)
	}
}

func TestDialKeyModeFailsClosedOnUnusableMaterial(t *testing.T) {
	server, dialFunc := startFixture(t, fakessh.Options{Password: "hunter2"})
	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice", Mode: appconnection.AuthModeKey, Password: "hunter2",
		SelectedKey: &appconnection.KeyMaterial{ID: "key-1", Label: "broken", PrivateKey: "not a key"},
	})
	_, err := dialWithPlan(t, dialFunc, plan, &fakeAuthPrompter{})
	assertKind(t, err, appconnection.KindAuth)
	if server.PublicKeyAttempts() != 0 || server.PasswordAttempts() != 0 {
		t.Fatalf("server saw auth attempts (%d publickey, %d password) despite fail-closed material",
			server.PublicKeyAttempts(), server.PasswordAttempts())
	}
}

func TestDialPasswordModeNeverOffersAPublicKey(t *testing.T) {
	userSigner, keyPEM := testSigner(t)
	server, dialFunc := startFixture(t, fakessh.Options{
		Password:   "never",
		PublicKeys: []xssh.PublicKey{userSigner.PublicKey()},
	})
	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice", Mode: appconnection.AuthModePassword, Password: "wrong",
		SelectedKey: &appconnection.KeyMaterial{ID: "key-1", Label: "work", PrivateKey: keyPEM},
		UseAgent:    true,
	})
	_, err := dialWithPlan(t, dialFunc, plan, &fakeAuthPrompter{})
	assertKind(t, err, appconnection.KindAuth)
	if server.PublicKeyAttempts() != 0 {
		t.Fatalf("password-only mode offered %d public keys", server.PublicKeyAttempts())
	}
}
