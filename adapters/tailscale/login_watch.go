// SPDX-License-Identifier: GPL-3.0-or-later

package tailscale

import (
	"context"
	"errors"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// startLoginWatchLocked starts the goroutine that waits for an interactive
// login to finish. The caller must hold p.mu. The watcher is provider-owned:
// Start and Cancel stop it before replacing the backend, and Close stops it
// before releasing the node.
func (p *Provider) startLoginWatchLocked(b backend) {
	if p.watchCancel != nil {
		// A watcher for this backend already exists. Overwriting it would leak
		// the goroutine and its done channel.
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	interval := p.watchInterval
	if interval <= 0 {
		interval = statusPollInterval
	}
	p.watchCancel = cancel
	p.watchDone = done
	go p.watchLogin(ctx, cancel, b, done, interval)
}

// stopLoginWatchLocked cancels the login watcher and returns a channel that
// closes when it exits. The caller must wait for it after releasing p.mu so a
// watcher never observes a retired backend.
func (p *Provider) stopLoginWatchLocked() <-chan struct{} {
	cancel := p.watchCancel
	done := p.watchDone
	p.watchCancel = nil
	p.watchDone = nil
	if cancel != nil {
		cancel()
	}
	return done
}

// watchLogin polls a NeedsLogin node until it reaches Running, fails, or is
// stopped. It publishes the Running transition so the Vault and the Connection
// Log see a completed login without an application restart.
func (p *Provider) watchLogin(ctx context.Context, cancel context.CancelFunc,
	b backend, done chan struct{}, interval time.Duration) {
	defer close(done)
	defer cancel()
	defer p.clearLoginWatch(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		status, err := b.Status(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// A transient LocalAPI read failure must not end the login wait.
			continue
		}
		switch status.Phase {
		case appconnection.ProviderPhaseRunning:
			if len(status.TailscaleIPs) == 0 {
				continue
			}
			p.mu.Lock()
			if p.backend != b || p.closed {
				p.mu.Unlock()
				return
			}
			p.applyStatusLocked(status, appconnection.ProviderPhaseRunning, nil)
			// The login watcher hands a running node back to the lifecycle
			// audit, so a later authorization loss or stop is still observed.
			p.startPollLocked(b)
			p.mu.Unlock()
			p.emit()
			return
		case appconnection.ProviderPhaseNeedsLogin:
			p.mu.Lock()
			if p.backend != b || p.closed {
				p.mu.Unlock()
				return
			}
			changed := p.status.AuthURL != status.AuthURL
			p.applyStatusLocked(status, appconnection.ProviderPhaseNeedsLogin, nil)
			p.mu.Unlock()
			if changed {
				p.emit()
			}
		case appconnection.ProviderPhaseError, appconnection.ProviderPhaseStopped:
			p.retireLoginBackend(b, status, status.Phase)
			return
		}
	}
}

// clearLoginWatch drops a finished watcher's bookkeeping without disturbing a
// newer watcher that a retry may already have installed.
func (p *Provider) clearLoginWatch(done chan struct{}) {
	p.mu.Lock()
	if p.watchDone == done {
		p.watchCancel = nil
		p.watchDone = nil
	}
	p.mu.Unlock()
}

// retireLoginBackend releases a node that failed or stopped while waiting for
// login, leaving the Provider retryable. The error carries no Auth URL.
func (p *Provider) retireLoginBackend(b backend, status backendStatus,
	phase appconnection.ProviderPhase) {
	cause := errors.New("tailscale: node failed while waiting for login")
	if phase == appconnection.ProviderPhaseStopped {
		cause = errors.New("tailscale: node stopped while waiting for login")
	}
	cause = redactError(cause, p.cfg.AuthKey)
	p.mu.Lock()
	if p.backend != b || p.closed {
		p.mu.Unlock()
		return
	}
	p.backend = nil
	// This goroutine owns the close; clear the watcher fields so a later
	// Cancel or Close does not wait on the channel it is about to close.
	p.watchCancel = nil
	p.watchDone = nil
	p.applyStatusLocked(status, phase, cause)
	p.mu.Unlock()
	p.emit()
	_ = b.Close()
}
