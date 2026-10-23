//go:build windows

package main

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

// No test uses WinINET. The token exercises the actual impersonation path,
// while the proxy records mutations without touching the user's settings.
type recordedWindowsProxy struct {
	enabled      bool
	owned        bool
	enableCalls  int
	disableCalls int
	enableError  error
}

func (p *recordedWindowsProxy) IsEnabled() bool { return p.enabled }

func (p *recordedWindowsProxy) Enable() error {
	p.enableCalls++
	if p.enableError != nil {
		return p.enableError
	}
	p.enabled, p.owned = true, true
	return nil
}

func (p *recordedWindowsProxy) Disable() error {
	p.disableCalls++
	p.enabled, p.owned = false, false
	return nil
}

func proxyOwnerTestToken(t *testing.T) (windows.Token, peerIdentity) {
	t.Helper()
	var primary windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &primary); err != nil {
		t.Fatal(err)
	}
	defer primary.Close()
	token, err := duplicateImpersonationToken(primary)
	if err != nil {
		t.Fatal(err)
	}
	userID, sessionID, err := impersonationTokenIdentity(token)
	if err != nil {
		token.Close()
		t.Fatal(err)
	}
	return token, peerIdentity{UserID: userID, SessionID: sessionID}
}

func TestWindowsProxyOwnerRepeatedAccessDoesNotWrite(t *testing.T) {
	previousListen := listenAddress
	t.Cleanup(func() { listenAddress = previousListen })
	for _, tc := range []struct {
		name       string
		local      bool
		preference bool
	}{
		{"named-pipe/enabled", false, true},
		{"named-pipe/disabled", false, false},
		{"local-listener/enabled", true, true},
		{"local-listener/disabled", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listenAddress = ""
			if tc.local {
				listenAddress = "127.0.0.1:1"
			}
			token, identity := proxyOwnerTestToken(t)
			defer token.Close()
			proxy := &recordedWindowsProxy{enabled: true, owned: false}
			p := &windowsPlatformInterface{ownerUserID: identity.UserID, sessionID: identity.SessionID, token: token, systemProxy: proxy, systemProxyEnabled: tc.preference}
			for i := 0; i < 100; i++ {
				if err := p.PrepareOwner(identity); err != nil {
					t.Fatal(err)
				}
				status, err := p.SystemProxyStatus()
				if err != nil || !status.Available || status.Enabled != tc.preference {
					t.Fatalf("status observation failed: %v, %v", status, err)
				}
			}
			if proxy.enableCalls != 0 || proxy.disableCalls != 0 || proxy.owned {
				t.Fatalf("read access rewrote external proxy state: enables=%d disables=%d", proxy.enableCalls, proxy.disableCalls)
			}
			if p.token != token || p.ownerUserID != identity.UserID || p.sessionID != identity.SessionID {
				t.Fatal("read access changed the owner session")
			}
		})
	}
}

func TestWindowsProxyExplicitEnableReassertsAfterExternalChange(t *testing.T) {
	previousListen := listenAddress
	listenAddress = ""
	t.Cleanup(func() { listenAddress = previousListen })
	token, identity := proxyOwnerTestToken(t)
	defer token.Close()
	// IsEnabled caches our last successful write, even after another client takes over.
	proxy := &recordedWindowsProxy{enabled: true, owned: false}
	p := &windowsPlatformInterface{ownerUserID: identity.UserID, sessionID: identity.SessionID, token: token, systemProxy: proxy, systemProxyEnabled: true}
	if err := p.PrepareOwner(identity); err != nil {
		t.Fatal(err)
	}
	if proxy.enableCalls != 0 {
		t.Fatal("authorization restored a proxy before the explicit request")
	}
	if err := p.SetSystemProxyEnabled(true); err != nil {
		t.Fatal(err)
	}
	if proxy.enableCalls != 1 || !proxy.owned {
		t.Fatal("explicit enable did not reassert the proxy")
	}
	if err := p.SetSystemProxyEnabled(false); err != nil {
		t.Fatal(err)
	}
	if proxy.disableCalls != 1 || proxy.enabled || p.systemProxyEnabled {
		t.Fatal("explicit disable did not clear the proxy")
	}
	proxy.enableError = errors.New("synthetic enable failure")
	if err := p.SetSystemProxyEnabled(true); !errors.Is(err, proxy.enableError) {
		t.Fatalf("explicit failure was hidden: %v", err)
	}
	if p.systemProxyEnabled || proxy.enabled {
		t.Fatal("failed explicit enable changed the previous disabled preference")
	}
}

func TestWindowsProxyOwnerReplacementAndReleaseStillApply(t *testing.T) {
	previousListen := listenAddress
	listenAddress = ""
	t.Cleanup(func() { listenAddress = previousListen })
	oldToken, identity := proxyOwnerTestToken(t)
	newToken, _ := proxyOwnerTestToken(t)
	proxy := &recordedWindowsProxy{enabled: true, owned: true}
	p := &windowsPlatformInterface{ownerUserID: identity.UserID, sessionID: identity.SessionID, token: oldToken, systemProxy: proxy, systemProxyEnabled: true}
	t.Cleanup(func() {
		if p.token != 0 {
			p.token.Close()
		}
	})
	if err := p.replaceOwnerTokenLocked(identity.UserID, identity.SessionID, newToken); err != nil {
		t.Fatal(err)
	}
	if proxy.disableCalls != 1 || proxy.enableCalls != 1 || !proxy.owned || p.token != newToken {
		t.Fatal("owner replacement did not transfer proxy state")
	}
	if err := p.PrepareOwner(identity); err != nil {
		t.Fatal(err)
	}
	if proxy.enableCalls != 1 || proxy.disableCalls != 1 {
		t.Fatal("subsequent authorization rewrote the replacement owner's proxy")
	}
	if err := p.ReleaseOwner(); err != nil {
		t.Fatal(err)
	}
	if proxy.disableCalls != 2 || p.token != 0 || p.ownerUserID != "" || p.sessionID != 0 {
		t.Fatal("owner release did not close its proxy and token")
	}
}
