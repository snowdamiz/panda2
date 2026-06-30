package http

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
	"github.com/sn0w/panda2/internal/clipevents"
	"github.com/sn0w/panda2/internal/repository"
	"github.com/sn0w/panda2/internal/store"
)

const (
	portalWSPingInterval = 30 * time.Second
	portalWSWriteWait    = 10 * time.Second
	portalWSReloadTTL    = 5 * time.Second
)

const (
	portalSessionTTL = 12 * time.Hour
	portalStateTTL   = 10 * time.Minute
)

// portalSession is the authenticated identity carried by a portal session token.
type portalSession struct {
	UserID   string
	Username string
	Avatar   string
}

type portalTokenPayload struct {
	UserID    string `json:"uid"`
	Username  string `json:"un,omitempty"`
	Avatar    string `json:"av,omitempty"`
	ExpiresAt int64  `json:"exp"`
}

type portalStatePayload struct {
	Nonce     string `json:"n"`
	ExpiresAt int64  `json:"exp"`
}

// signHMAC returns base64url(payload) + "." + base64url(hmac(payload)).
func signHMAC(secret string, payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return body + "." + sig, nil
}

// verifyHMAC validates the signature and decodes the payload into target.
func verifyHMAC(secret, token string, target any) bool {
	body, sig, found := strings.Cut(strings.TrimSpace(token), ".")
	if !found || body == "" || sig == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	expected := mac.Sum(nil)
	provided, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || subtle.ConstantTimeCompare(expected, provided) != 1 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, target) == nil
}

func (s *Server) signPortalToken(session portalSession) (string, error) {
	return signHMAC(s.cfg.PortalSessionSecret, portalTokenPayload{
		UserID:    session.UserID,
		Username:  session.Username,
		Avatar:    session.Avatar,
		ExpiresAt: time.Now().UTC().Add(portalSessionTTL).Unix(),
	})
}

func (s *Server) signPortalState() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return signHMAC(s.cfg.PortalSessionSecret, portalStatePayload{
		Nonce:     base64.RawURLEncoding.EncodeToString(nonce[:]),
		ExpiresAt: time.Now().UTC().Add(portalStateTTL).Unix(),
	})
}

func (s *Server) verifyPortalState(state string) bool {
	var payload portalStatePayload
	if !verifyHMAC(s.cfg.PortalSessionSecret, state, &payload) {
		return false
	}
	return time.Now().UTC().Unix() <= payload.ExpiresAt
}

// requireUser authenticates the request against a portal session token.
func (s *Server) requireUser(c *fiber.Ctx) (portalSession, error) {
	if strings.TrimSpace(s.cfg.PortalSessionSecret) == "" {
		return portalSession{}, c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "portal_unconfigured"})
	}
	token := bearerToken(c)
	if token == "" {
		return portalSession{}, c.Status(fiber.StatusUnauthorized).JSON(map[string]string{"error": "portal_unauthorized"})
	}
	var payload portalTokenPayload
	if !verifyHMAC(s.cfg.PortalSessionSecret, token, &payload) {
		return portalSession{}, c.Status(fiber.StatusUnauthorized).JSON(map[string]string{"error": "portal_unauthorized"})
	}
	if strings.TrimSpace(payload.UserID) == "" || time.Now().UTC().Unix() > payload.ExpiresAt {
		return portalSession{}, c.Status(fiber.StatusUnauthorized).JSON(map[string]string{"error": "portal_unauthorized"})
	}
	return portalSession{UserID: payload.UserID, Username: payload.Username, Avatar: payload.Avatar}, nil
}

// sessionFromToken validates a raw portal session token (used where an
// Authorization header isn't available, e.g. the WebSocket query param) and
// returns the identity it carries.
func (s *Server) sessionFromToken(token string) (portalSession, bool) {
	if strings.TrimSpace(s.cfg.PortalSessionSecret) == "" || strings.TrimSpace(token) == "" {
		return portalSession{}, false
	}
	var payload portalTokenPayload
	if !verifyHMAC(s.cfg.PortalSessionSecret, token, &payload) {
		return portalSession{}, false
	}
	if strings.TrimSpace(payload.UserID) == "" || time.Now().UTC().Unix() > payload.ExpiresAt {
		return portalSession{}, false
	}
	return portalSession{UserID: payload.UserID, Username: payload.Username, Avatar: payload.Avatar}, true
}

// discordPortalLogin redirects the browser to Discord's identify OAuth flow.
func (s *Server) discordPortalLogin(c *fiber.Ctx) error {
	if !s.cfg.PortalConfigured() || s.portalOAuth == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	state, err := s.signPortalState()
	if err != nil {
		slog.Error("portal login state sign failed", "error", err)
		return c.SendStatus(fiber.StatusInternalServerError)
	}
	return c.Redirect(s.portalOAuth.AuthorizeURL(state), fiber.StatusFound)
}

// discordPortalCallback completes the OAuth flow, mints a session token, and
// hands it back to the portal page via a URL fragment.
func (s *Server) discordPortalCallback(c *fiber.Ctx) error {
	if !s.cfg.PortalConfigured() || s.portalOAuth == nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}
	if oauthErr := strings.TrimSpace(c.Query("error")); oauthErr != "" {
		return s.redirectPortal(c, "error=access_denied")
	}
	if !s.verifyPortalState(c.Query("state")) {
		return s.redirectPortal(c, "error=invalid_state")
	}
	identity, err := s.portalOAuth.Exchange(c.Context(), c.Query("code"))
	if err != nil {
		slog.Warn("portal oauth exchange failed", "error", err)
		return s.redirectPortal(c, "error=login_failed")
	}
	token, err := s.signPortalToken(portalSession{
		UserID:   identity.ID,
		Username: identity.DisplayName(),
		Avatar:   identity.Avatar,
	})
	if err != nil {
		slog.Error("portal token sign failed", "error", err)
		return s.redirectPortal(c, "error=login_failed")
	}
	return s.redirectPortal(c, "token="+token)
}

// redirectPortal sends the browser back to the portal /clips page with the given
// URL fragment. Falls back to a JSON body when no portal base URL is set.
func (s *Server) redirectPortal(c *fiber.Ctx, fragment string) error {
	base := strings.TrimRight(strings.TrimSpace(s.cfg.PortalBaseURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(s.cfg.PublicAppURL), "/")
	}
	if base == "" {
		status, value, _ := strings.Cut(fragment, "=")
		return c.JSON(map[string]string{status: value})
	}
	return c.Redirect(base+"/clips#"+fragment, fiber.StatusFound)
}

func (s *Server) portalMe(c *fiber.Ctx) error {
	session, err := s.requireUser(c)
	if err != nil {
		return err
	}
	return c.JSON(map[string]any{
		"user_id":  session.UserID,
		"username": session.Username,
		"avatar":   session.Avatar,
	})
}

func (s *Server) portalListClips(c *fiber.Ctx) error {
	session, err := s.requireUser(c)
	if err != nil {
		return err
	}
	if s.clips == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "clips_unavailable"})
	}
	clips, err := s.clips.ListByUser(c.Context(), session.UserID, 0, 0)
	if err != nil {
		slog.Error("portal list clips failed", "error", err, "user_id", session.UserID)
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{"error": "internal_error"})
	}
	items := make([]map[string]any, 0, len(clips))
	for _, clip := range clips {
		items = append(items, s.clipToJSON(clip))
	}
	return c.JSON(map[string]any{"clips": items})
}

// clipToJSON serializes a clip for the portal API, minting a fresh presigned
// thumbnail URL when signing is configured. Shared by the list endpoint and the
// live WebSocket so both emit identically shaped clips.
func (s *Server) clipToJSON(clip store.YoutubeClip) map[string]any {
	thumbnailURL := ""
	if s.clipStore != nil && s.clipStore.SigningConfigured() && strings.TrimSpace(clip.ThumbnailObjectKey) != "" {
		if signed, err := s.clipStore.PresignGetURL(clip.ThumbnailObjectKey, s.cfg.R2DownloadURLTTL); err == nil {
			thumbnailURL = signed
		}
	}
	return map[string]any{
		"id":               clip.ID,
		"title":            clip.Title,
		"type":             clip.ClipType,
		"rank":             clip.Rank,
		"duration_seconds": clip.DurationSeconds,
		"thumbnail_url":    thumbnailURL,
		"video_title":      clip.VideoTitle,
		"video_url":        clip.VideoURL,
		"video_uploader":   clip.VideoUploader,
		"size_bytes":       clip.SizeBytes,
		"virality_score":   clip.ViralityScore,
		"hook_score":       clip.HookScore,
		"retention_score":  clip.RetentionScore,
		"created_at":       clip.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) portalClipURL(c *fiber.Ctx) error {
	session, err := s.requireUser(c)
	if err != nil {
		return err
	}
	if s.clips == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "clips_unavailable"})
	}
	id := strings.TrimSpace(c.Params("id"))
	clip, ok, err := s.clips.GetByIDForUser(c.Context(), id, session.UserID)
	if err != nil {
		slog.Error("portal clip lookup failed", "error", err, "user_id", session.UserID, "clip_id", id)
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{"error": "internal_error"})
	}
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(map[string]string{"error": "not_found"})
	}
	if s.clipStore == nil || !s.clipStore.SigningConfigured() {
		return c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "downloads_unavailable"})
	}
	filename := clipDownloadFilename(clip.Title, clip.Rank)
	signed, err := s.clipStore.PresignDownloadURL(clip.ObjectKey, s.cfg.R2DownloadURLTTL, filename)
	if err != nil {
		slog.Error("portal clip presign failed", "error", err, "clip_id", clip.ID)
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{"error": "internal_error"})
	}
	// stream is a plain GET URL (no attachment disposition) so the player can
	// play it inline in a <video> element. Falls back to the download URL.
	stream, streamErr := s.clipStore.PresignGetURL(clip.ObjectKey, s.cfg.R2DownloadURLTTL)
	if streamErr != nil {
		slog.Error("portal clip stream presign failed", "error", streamErr, "clip_id", clip.ID)
		stream = signed
	}
	return c.JSON(map[string]any{
		"url":        signed,
		"stream_url": stream,
		"title":      clip.Title,
		"filename":   filename,
	})
}

func (s *Server) portalDeleteClip(c *fiber.Ctx) error {
	session, err := s.requireUser(c)
	if err != nil {
		return err
	}
	if s.clips == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(map[string]string{"error": "clips_unavailable"})
	}
	id := strings.TrimSpace(c.Params("id"))
	clip, err := s.clips.DeleteByIDForUser(c.Context(), id, session.UserID)
	if errors.Is(err, repository.ErrNotFound) {
		return c.Status(fiber.StatusNotFound).JSON(map[string]string{"error": "not_found"})
	}
	if err != nil {
		slog.Error("portal clip delete failed", "error", err, "user_id", session.UserID, "clip_id", id)
		return c.Status(fiber.StatusInternalServerError).JSON(map[string]string{"error": "internal_error"})
	}
	if s.clipStore != nil && s.clipStore.SigningConfigured() {
		if key := strings.TrimSpace(clip.ObjectKey); key != "" {
			if delErr := s.clipStore.Delete(c.Context(), key); delErr != nil {
				slog.Error("portal clip object delete failed", "error", delErr, "clip_id", clip.ID, "object_key", key)
			}
		}
		if key := strings.TrimSpace(clip.ThumbnailObjectKey); key != "" {
			if delErr := s.clipStore.Delete(c.Context(), key); delErr != nil {
				slog.Error("portal clip thumbnail delete failed", "error", delErr, "clip_id", clip.ID, "object_key", key)
			}
		}
	}
	// Tell any other live session for this user (another tab/device) to drop it.
	if s.clipEvents != nil {
		s.clipEvents.PublishClipDeleted(session.UserID, clip.ID)
	}
	return c.JSON(map[string]any{"status": "deleted", "id": clip.ID})
}

// portalClipsWSUpgrade authenticates the WebSocket handshake (the token rides
// in the query string since browsers can't set headers on a socket) and gates
// the upgrade, stashing the verified user id for the socket handler.
func (s *Server) portalClipsWSUpgrade(c *fiber.Ctx) error {
	if !websocket.IsWebSocketUpgrade(c) {
		return fiber.ErrUpgradeRequired
	}
	session, ok := s.sessionFromToken(c.Query("token"))
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(map[string]string{"error": "portal_unauthorized"})
	}
	c.Locals("portalUserID", session.UserID)
	return c.Next()
}

// portalClipsWS streams clip lifecycle events to a signed-in browser so the
// library stays live without polling. The socket is read-only from the client's
// side; all mutations still flow through the REST endpoints.
func (s *Server) portalClipsWS(conn *websocket.Conn) {
	defer conn.Close()
	userID, _ := conn.Locals("portalUserID").(string)
	if userID == "" || s.clipEvents == nil {
		return
	}

	events, cancel := s.clipEvents.Subscribe(userID)
	defer cancel()

	// Detect the client going away (and drain any control frames) in a reader
	// goroutine; the writer loop below owns all sends, so there's no write race.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(portalWSPingInterval)
	defer ping.Stop()
	for {
		select {
		case <-closed:
			return
		case <-ping.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(portalWSWriteWait)); err != nil {
				return
			}
		case ev := <-events:
			payload := s.clipEventPayload(userID, ev)
			if payload == nil {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(portalWSWriteWait))
			if err := conn.WriteJSON(payload); err != nil {
				return
			}
		}
	}
}

// clipEventPayload turns an internal clip event into the JSON the browser
// consumes. For creations it reloads the clip so the pushed shape matches the
// list endpoint (including a fresh presigned thumbnail); a clip that's already
// gone yields no payload.
func (s *Server) clipEventPayload(userID string, ev clipevents.Event) map[string]any {
	switch ev.Type {
	case clipevents.ClipCreated:
		if s.clips == nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), portalWSReloadTTL)
		defer cancel()
		clip, ok, err := s.clips.GetByIDForUser(ctx, ev.ClipID, userID)
		if err != nil || !ok {
			return nil
		}
		return map[string]any{"type": clipevents.ClipCreated, "clip": s.clipToJSON(clip)}
	case clipevents.ClipDeleted:
		return map[string]any{"type": clipevents.ClipDeleted, "id": ev.ClipID}
	default:
		return nil
	}
}

// clipDownloadFilename builds a friendly download filename for a clip.
func clipDownloadFilename(title string, rank int) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == ' ', r == '-', r == '_':
			return '-'
		default:
			return -1
		}
	}, strings.TrimSpace(title))
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		cleaned = "clip"
	}
	if len(cleaned) > 60 {
		cleaned = cleaned[:60]
	}
	return cleaned + ".mp4"
}
