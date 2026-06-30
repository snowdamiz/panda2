package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const discordPortalUserURL = "https://discord.com/api/v10/users/@me"

// PortalIdentity is the minimal Discord profile resolved for the clips portal
// via the `identify` OAuth scope.
type PortalIdentity struct {
	ID         string
	Username   string
	GlobalName string
	Avatar     string
}

// DisplayName returns the best human-facing name for the identity.
func (p PortalIdentity) DisplayName() string {
	if name := strings.TrimSpace(p.GlobalName); name != "" {
		return name
	}
	return strings.TrimSpace(p.Username)
}

// PortalOAuthClient performs the Discord-login OAuth flow used by the clips
// portal. Unlike the bot install flow it requests only the `identify` scope and
// resolves the authenticated user's profile.
type PortalOAuthClient struct {
	client       *http.Client
	clientID     string
	clientSecret string
	redirectURI  string
}

func NewPortalOAuthClient(clientID, clientSecret, redirectURI string) *PortalOAuthClient {
	return &PortalOAuthClient{
		client:       http.DefaultClient,
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		redirectURI:  strings.TrimSpace(redirectURI),
	}
}

// Configured reports whether the client has the credentials needed to run the
// OAuth flow.
func (c *PortalOAuthClient) Configured() bool {
	return c != nil && c.clientID != "" && c.clientSecret != "" && c.redirectURI != ""
}

// AuthorizeURL builds the Discord authorize URL for the identify-only login.
func (c *PortalOAuthClient) AuthorizeURL(state string) string {
	values := url.Values{}
	values.Set("client_id", c.clientID)
	values.Set("scope", "identify")
	values.Set("response_type", "code")
	values.Set("redirect_uri", c.redirectURI)
	values.Set("state", state)
	values.Set("prompt", "consent")
	return "https://discord.com/oauth2/authorize?" + values.Encode()
}

// Exchange swaps an authorization code for the authenticated user's identity.
func (c *PortalOAuthClient) Exchange(ctx context.Context, code string) (PortalIdentity, error) {
	if !c.Configured() {
		return PortalIdentity{}, fmt.Errorf("discord portal oauth is not configured")
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return PortalIdentity{}, fmt.Errorf("authorization code is required")
	}
	client := c.client
	if client == nil {
		client = http.DefaultClient
	}

	form := url.Values{}
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", c.redirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, discordOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return PortalIdentity{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return PortalIdentity{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return PortalIdentity{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PortalIdentity{}, fmt.Errorf("discord oauth exchange failed with status %d", resp.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return PortalIdentity{}, err
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return PortalIdentity{}, fmt.Errorf("discord oauth exchange returned no access token")
	}

	identity, err := c.currentUser(ctx, client, token.AccessToken)
	if err != nil {
		return PortalIdentity{}, err
	}
	if strings.TrimSpace(identity.ID) == "" {
		return PortalIdentity{}, fmt.Errorf("discord oauth could not identify the user")
	}
	return identity, nil
}

func (c *PortalOAuthClient) currentUser(ctx context.Context, client *http.Client, accessToken string) (PortalIdentity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discordPortalUserURL, nil)
	if err != nil {
		return PortalIdentity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return PortalIdentity{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return PortalIdentity{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PortalIdentity{}, fmt.Errorf("discord user identity lookup failed with status %d", resp.StatusCode)
	}
	var user struct {
		ID         string `json:"id"`
		Username   string `json:"username"`
		GlobalName string `json:"global_name"`
		Avatar     string `json:"avatar"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return PortalIdentity{}, err
	}
	return PortalIdentity{
		ID:         strings.TrimSpace(user.ID),
		Username:   strings.TrimSpace(user.Username),
		GlobalName: strings.TrimSpace(user.GlobalName),
		Avatar:     strings.TrimSpace(user.Avatar),
	}, nil
}
