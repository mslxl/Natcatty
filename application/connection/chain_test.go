package connection

import (
	"context"
	"errors"
	"testing"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
)

// hopAuthSource returns a plan per Host and records the order it was asked.
type hopAuthSource struct {
	plans map[model.HostID]AuthPlan
	errAt model.HostID
	err   error
	reqs  []Request
}

func (s *hopAuthSource) AuthPlan(_ context.Context, req Request) (AuthPlan, error) {
	s.reqs = append(s.reqs, req)
	if s.err != nil && req.HostID == s.errAt {
		return AuthPlan{}, s.err
	}
	return s.plans[req.HostID], nil
}

// chainDialer records the full chain request and can answer with a typed
// failure.
type chainDialer struct {
	request  DialRequest
	progress []ChainProgress
	err      error
	verify   map[string]domainconn.Key
}

func (d *chainDialer) Dial(ctx context.Context, req DialRequest) (Client, error) {
	d.request = req
	if req.OnProgress != nil {
		d.progress = append(d.progress, ChainProgress{Index: 1, Total: len(req.Jumps) + 1, Status: ChainConnecting})
	}
	if d.err != nil {
		return nil, d.err
	}
	// Verify every endpoint with the callback and credentials that endpoint
	// supplied, exactly as the transport adapter does.
	if req.VerifyHostKey != nil {
		if err := req.VerifyHostKey(ctx, d.verify["target"]); err != nil {
			return nil, err
		}
	}
	for i, jump := range req.Jumps {
		key := d.verify["jump"]
		key.Hostname = jump.Hostname
		key.Port = jump.Port
		if jump.VerifyHostKey != nil {
			if err := jump.VerifyHostKey(ctx, key); err != nil {
				return nil, err
			}
		}
		if jump.OnAuth != nil {
			jump.OnAuth("password-" + string(rune('1'+i)))
		}
	}
	if req.OnAuth != nil {
		req.OnAuth(CandidatePassword)
	}
	return &fakeClient{}, nil
}

func chainRequest() Request {
	return Request{
		HostID: "target", Hostname: "target.example.com", Port: 22, Username: "root",
		Jumps: []Hop{
			{HostID: "jump-1", Label: "bastion", Hostname: "bastion.example.com", Port: 2222, Username: "ops"},
			{HostID: "jump-2", Hostname: "inner.example.com", Port: 22, Username: "deploy"},
		},
		Proxy:      &Proxy{Type: ProxyHTTP, Host: "proxy.example.com", Port: 8080, Username: "u", Password: "p"},
		OnProgress: func(ChainProgress) {},
	}
}

func TestAcquireInheritsLegacyAlgorithmsAndKeepsOverridesPerHop(t *testing.T) {
	source := &hopAuthSource{plans: map[model.HostID]AuthPlan{
		"jump-1": BuildAuthPlan(AuthSpec{Username: "ops", Mode: AuthModePassword, Password: "1"}),
		"jump-2": BuildAuthPlan(AuthSpec{Username: "deploy", Mode: AuthModePassword, Password: "2"}),
		"target": BuildAuthPlan(AuthSpec{Username: "root", Mode: AuthModePassword, Password: "3"}),
	}}
	dialer := &chainDialer{verify: map[string]domainconn.Key{
		"jump":   {Hostname: "bastion.example.com", Port: 2222, KeyType: "ssh-ed25519", Fingerprint: "jump-key"},
		"target": {Hostname: "target.example.com", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "target-key"},
	}}
	manager := NewManager(dialer, nil, &fakeKnownHosts{}, WithAuthSource(source),
		WithPrompter(&fakePrompter{decision: PromptDecision{Trust: true}}))

	req := chainRequest()
	req.Algorithms = AlgorithmPolicy{
		Legacy:           true,
		SkipECDSAHostKey: true,
		Ciphers:          []string{"aes256-ctr"},
	}
	req.Jumps[0].Algorithms = AlgorithmPolicy{
		SkipECDSAHostKey: true,
		Ciphers:          []string{"aes128-cbc"},
	}

	lease, err := manager.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = lease.Release()

	request := dialer.request
	if !request.Algorithms.Legacy || !request.Algorithms.SkipECDSAHostKey {
		t.Fatalf("target algorithms = %+v", request.Algorithms)
	}
	if !request.Jumps[0].Algorithms.Legacy {
		t.Fatal("the first hop did not inherit legacy algorithms from the target")
	}
	if !request.Jumps[0].Algorithms.SkipECDSAHostKey ||
		len(request.Jumps[0].Algorithms.Ciphers) != 1 || request.Jumps[0].Algorithms.Ciphers[0] != "aes128-cbc" {
		t.Fatalf("first hop algorithms = %+v", request.Jumps[0].Algorithms)
	}
	if !request.Jumps[1].Algorithms.Legacy {
		t.Fatal("the second hop did not inherit legacy algorithms from the target")
	}
	if request.Jumps[1].Algorithms.SkipECDSAHostKey {
		t.Fatal("the second hop must not inherit the target's ECDSA skip")
	}
	if len(request.Jumps[1].Algorithms.Ciphers) != 0 {
		t.Fatalf("the second hop must not inherit the target's cipher override: %+v", request.Jumps[1].Algorithms)
	}
}

func TestAcquireResolvesEveryHopWithItsOwnAuth(t *testing.T) {
	source := &hopAuthSource{plans: map[model.HostID]AuthPlan{
		"jump-1": BuildAuthPlan(AuthSpec{Username: "alice", Mode: AuthModePassword, Password: "jump-one"}),
		"jump-2": BuildAuthPlan(AuthSpec{Username: "bob", Mode: AuthModePassword, Password: "jump-two"}),
		"target": BuildAuthPlan(AuthSpec{Username: "carol", Mode: AuthModePassword, Password: "target-pw"}),
	}}
	known := &fakeKnownHosts{records: []domainconn.KnownHost{
		{Hostname: "bastion.example.com", Port: 2222, KeyType: "ssh-ed25519", Fingerprint: "trusted"},
		{Hostname: "inner.example.com", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "trusted"},
	}}
	prompter := &fakePrompter{decision: PromptDecision{Trust: true}}
	dialer := &chainDialer{verify: map[string]domainconn.Key{
		"jump":   {Hostname: "bastion.example.com", Port: 2222, KeyType: "ssh-ed25519", Fingerprint: "trusted"},
		"target": {Hostname: "target.example.com", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "target-key"},
	}}
	manager := NewManager(dialer, nil, known, WithAuthSource(source), WithPrompter(prompter))

	lease, err := manager.Acquire(context.Background(), chainRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = lease.Release()

	if len(source.reqs) != 3 {
		t.Fatalf("AuthSource calls = %d, want 3 (two jumps and the target)", len(source.reqs))
	}
	if source.reqs[0].HostID != "jump-1" || source.reqs[1].HostID != "jump-2" || source.reqs[2].HostID != "target" {
		t.Fatalf("AuthSource order = %v,%v,%v", source.reqs[0].HostID, source.reqs[1].HostID, source.reqs[2].HostID)
	}

	request := dialer.request
	if len(request.Jumps) != 2 {
		t.Fatalf("jumps = %d, want 2", len(request.Jumps))
	}
	if request.Jumps[0].Label != "bastion" || request.Jumps[0].Hostname != "bastion.example.com" ||
		request.Jumps[0].Port != 2222 || request.Jumps[0].Username != "alice" ||
		request.Jumps[0].Password != "jump-one" {
		t.Fatalf("first hop = %+v", request.Jumps[0])
	}
	if request.Jumps[1].Label != "inner.example.com:22" || request.Jumps[1].Username != "bob" ||
		request.Jumps[1].Password != "jump-two" {
		t.Fatalf("second hop = %+v", request.Jumps[1])
	}
	if request.Username != "carol" || request.Password != "target-pw" {
		t.Fatalf("target = %s/%s, want carol/target-pw", request.Username, request.Password)
	}
	if request.Label != "target.example.com:22" {
		t.Fatalf("target label = %q, want the same address annotation auth failures use", request.Label)
	}
	if request.Proxy == nil || request.Proxy.Type != ProxyHTTP || request.Proxy.Host != "proxy.example.com" {
		t.Fatalf("proxy = %+v", request.Proxy)
	}
	if request.Jumps[0].VerifyHostKey == nil || request.VerifyHostKey == nil {
		t.Fatal("a per-hop host-key callback is missing")
	}

	// The trusted bastion key needs no prompt; the unknown target asks once.
	if len(prompter.prompted()) != 1 || prompter.prompted()[0].Hostname != "target.example.com" {
		t.Fatalf("prompts = %+v, want one target prompt", prompter.prompted())
	}

	if len(dialer.progress) != 1 {
		t.Fatalf("chain progress events = %d, want the forwarded callback", len(dialer.progress))
	}
	if progress := dialer.progress[0]; progress.Index != 1 || progress.Total != 3 ||
		progress.Status != ChainConnecting {
		t.Fatalf("chain progress = %+v, want the first of three endpoints", progress)
	}
	for i, hop := range []string{"alice@bastion.example.com:2222", "bob@inner.example.com:22", "carol@target.example.com:22"} {
		key := authCacheKey(source.reqs[i].Username, source.reqs[i].Hostname, source.reqs[i].Port)
		if _, ok := manager.authCache.Get(key); !ok {
			t.Errorf("no cached method for %s", hop)
		}
	}
}

func TestJumpAuthFailureIsTaggedAndClearsOnlyThatHop(t *testing.T) {
	source := &hopAuthSource{plans: map[model.HostID]AuthPlan{
		"jump-1": BuildAuthPlan(AuthSpec{Username: "a", Mode: AuthModePassword, Password: "1"}),
		"jump-2": BuildAuthPlan(AuthSpec{Username: "b", Mode: AuthModePassword, Password: "2"}),
		"target": BuildAuthPlan(AuthSpec{Username: "c", Mode: AuthModePassword, Password: "3"}),
	}}
	dialer := &chainDialer{err: &Error{
		Kind: KindAuth, Cause: ErrAuthentication,
		Hop: &HopInfo{Index: 2, Total: 3, Label: "inner.example.com:22"},
	}}
	manager := NewManager(dialer, nil, &fakeKnownHosts{}, WithAuthSource(source))
	manager.authCache.Put("ops@bastion.example.com:2222", CandidateAgent)
	manager.authCache.Put("deploy@inner.example.com:22", CandidateAgent)
	manager.authCache.Put("root@target.example.com:22", CandidateAgent)

	_, err := manager.Acquire(context.Background(), chainRequest())
	assertKind(t, err, KindAuth)
	var typed *Error
	if !errors.As(err, &typed) || typed.Hop == nil || typed.Hop.Index != 2 || typed.Hop.Total != 3 {
		t.Fatalf("error hop = %+v", typed)
	}
	if _, ok := manager.authCache.Get("deploy@inner.example.com:22"); ok {
		t.Fatal("the failed hop's cached method was not cleared")
	}
	if _, ok := manager.authCache.Get("ops@bastion.example.com:2222"); !ok {
		t.Fatal("the first hop's cached method was cleared")
	}
	if _, ok := manager.authCache.Get("root@target.example.com:22"); !ok {
		t.Fatal("the target's cached method was cleared")
	}
}

func TestHopAuthResolutionFailureCarriesHopContext(t *testing.T) {
	source := &hopAuthSource{
		plans: map[model.HostID]AuthPlan{},
		errAt: "jump-2",
		err:   AuthFailure(ErrKeyUnavailable),
	}
	manager := NewManager(&chainDialer{}, nil, &fakeKnownHosts{}, WithAuthSource(source))

	_, err := manager.Acquire(context.Background(), chainRequest())
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("error = %v, want a typed error", err)
	}
	if typed.Kind != KindAuth || typed.Hop == nil || typed.Hop.Index != 2 || typed.Hop.Total != 3 {
		t.Fatalf("error = %+v", typed)
	}
	if typed.Hop.Label != "inner.example.com:22" {
		t.Fatalf("hop label = %q", typed.Hop.Label)
	}
}

func TestKnownHostReadFailuresKeepTheirKind(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Kind
	}{
		{"cancelled", context.Canceled, KindCancelled},
		{"timeout", context.DeadlineExceeded, KindTimeout},
		{"store", errors.New("database unavailable"), KindInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager(&chainDialer{}, nil, &fakeKnownHosts{listErr: test.err})
			_, err := manager.Acquire(context.Background(), chainRequest())
			assertKind(t, err, test.want)
			if !errors.Is(err, test.err) {
				t.Fatalf("error = %v, want it to wrap %v", err, test.err)
			}
		})
	}
}

func TestAcquireWithoutJumpsKeepsTheSingleEndpointShape(t *testing.T) {
	source := &hopAuthSource{plans: map[model.HostID]AuthPlan{
		"target": BuildAuthPlan(AuthSpec{Username: "root", Mode: AuthModePassword, Password: "pw"}),
	}}
	dialer := &chainDialer{verify: map[string]domainconn.Key{
		"target": {Hostname: "example.com", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "trusted"},
	}}
	known := &fakeKnownHosts{records: []domainconn.KnownHost{
		{Hostname: "example.com", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "trusted"},
	}}
	manager := NewManager(dialer, nil, known, WithAuthSource(source))

	lease, err := manager.Acquire(context.Background(), Request{
		HostID: "target", Hostname: "example.com", Port: 22, Username: "root",
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = lease.Release()
	if len(dialer.request.Jumps) != 0 || dialer.request.Hostname != "example.com" ||
		dialer.request.Password != "pw" || dialer.request.Proxy != nil {
		t.Fatalf("dial request = %+v", dialer.request)
	}
}

func TestHopLabelFallsBackToTheEndpoint(t *testing.T) {
	tests := []struct {
		hop  Hop
		want string
	}{
		{Hop{Label: "bastion", Hostname: "ignored", Port: 1}, "bastion"},
		{Hop{Hostname: "example.com", Port: 2222}, "example.com:2222"},
		{Hop{Hostname: "2001:db8::1", Port: 22}, "[2001:db8::1]:22"},
		{Hop{Hostname: "example.com"}, "example.com:22"},
	}
	for _, test := range tests {
		if got := test.hop.LabelOrAddress(); got != test.want {
			t.Errorf("LabelOrAddress(%+v) = %q, want %q", test.hop, got, test.want)
		}
	}
}
