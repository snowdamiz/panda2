package http

import (
	stdhttp "net/http"
	"testing"
	"time"

	"github.com/sn0w/panda2/internal/config"
	discordbot "github.com/sn0w/panda2/internal/discord"
)

func newPortalServer(secret string) *Server {
	return New(config.Config{
		PortalSessionSecret: secret,
		Environment:         "production",
	}, nil)
}

func TestPortalTokenRoundTrip(t *testing.T) {
	server := newPortalServer("portal-secret")
	token, err := server.signPortalToken(portalSession{UserID: "123", Username: "Ada", Avatar: "abc"})
	if err != nil {
		t.Fatalf("signPortalToken: %v", err)
	}
	var payload portalTokenPayload
	if !verifyHMAC(server.cfg.PortalSessionSecret, token, &payload) {
		t.Fatal("expected token to verify")
	}
	if payload.UserID != "123" || payload.Username != "Ada" || payload.Avatar != "abc" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if payload.ExpiresAt <= time.Now().UTC().Unix() {
		t.Fatal("expected a future expiry")
	}
}

func TestPortalTokenRejectsTamperAndWrongSecret(t *testing.T) {
	server := newPortalServer("portal-secret")
	token, err := server.signPortalToken(portalSession{UserID: "123"})
	if err != nil {
		t.Fatalf("signPortalToken: %v", err)
	}

	var payload portalTokenPayload
	if verifyHMAC("other-secret", token, &payload) {
		t.Fatal("token must not verify under a different secret")
	}

	tampered := token + "x"
	if verifyHMAC(server.cfg.PortalSessionSecret, tampered, &payload) {
		t.Fatal("tampered token must not verify")
	}

	if verifyHMAC(server.cfg.PortalSessionSecret, "no-dot-here", &payload) {
		t.Fatal("malformed token must not verify")
	}
}

func TestPortalStateRoundTripAndExpiry(t *testing.T) {
	server := newPortalServer("portal-secret")
	state, err := server.signPortalState()
	if err != nil {
		t.Fatalf("signPortalState: %v", err)
	}
	if !server.verifyPortalState(state) {
		t.Fatal("expected fresh state to verify")
	}
	if server.verifyPortalState("garbage") {
		t.Fatal("garbage state must not verify")
	}

	// An expired-but-correctly-signed state is rejected.
	expired, err := signHMAC(server.cfg.PortalSessionSecret, portalStatePayload{
		Nonce:     "n",
		ExpiresAt: time.Now().UTC().Add(-time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("signHMAC: %v", err)
	}
	if server.verifyPortalState(expired) {
		t.Fatal("expired state must not verify")
	}
}

func TestDiscordPortalCallbackRedirectsToCanonicalClipsURL(t *testing.T) {
	server := New(config.Config{
		DiscordApplicationID:     "app-id",
		DiscordClientSecret:      "secret",
		DiscordPortalRedirectURI: "https://api.example.test/auth/discord/callback",
		PortalBaseURL:            "https://panda.example.test/",
		PortalSessionSecret:      "portal-secret",
		Environment:              "production",
	}, nil).WithPortalOAuth(discordbot.NewPortalOAuthClient(
		"app-id",
		"secret",
		"https://api.example.test/auth/discord/callback",
	))

	req, err := stdhttp.NewRequest(stdhttp.MethodGet, "/auth/discord/callback?state=bad", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := server.Test(req)
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusFound {
		t.Fatalf("expected redirect, got status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "https://panda.example.test/clips/#error=invalid_state" {
		t.Fatalf("unexpected redirect location: %q", got)
	}
}
