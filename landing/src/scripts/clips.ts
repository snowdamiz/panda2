// Clips portal controller.
//
// Runs on /clips. Completes the Discord OAuth token handoff (the API redirects
// back with the session token in the URL fragment), then lists, plays,
// downloads, and removes the signed-in user's generated clips via the
// authenticated /portal API. Safe to call on every page — it no-ops unless
// [data-clips-root] exists.

const portalSessionKey = 'panda.portalSession.v1';

type PortalUser = {
  user_id: string;
  username: string;
  avatar: string;
};

type PortalClip = {
  id: string;
  title: string;
  type: string;
  rank: number;
  duration_seconds: number;
  thumbnail_url: string;
  video_title: string;
  video_url: string;
  video_uploader: string;
  size_bytes: number;
  virality_score: number;
  hook_score: number;
  retention_score: number;
  created_at: string;
};

type ClipsListResponse = { clips: PortalClip[] };
type ClipURLResponse = { url: string; stream_url?: string; filename: string; title?: string };

type StatusTone = 'neutral' | 'error';

export const initClipsPortal = (): void => {
  const root = document.querySelector<HTMLElement>('[data-clips-root]');
  if (!root) return;
  new ClipsPortalController(root).init();
};

class ClipsPortalController {
  private readonly apiBase: string;
  private readonly gate: HTMLElement | null;
  private readonly dashboard: HTMLElement | null;
  private readonly list: HTMLElement | null;
  private readonly empty: HTMLElement | null;
  private readonly count: HTMLElement | null;
  private readonly status: HTMLElement | null;
  private readonly userLabel: HTMLElement | null;

  private readonly modal: HTMLDialogElement | null;
  private readonly modalVideo: HTMLVideoElement | null;
  private readonly modalTitle: HTMLElement | null;
  private readonly modalScores: HTMLElement | null;
  private readonly modalMeta: HTMLElement | null;
  private readonly modalDownload: HTMLElement | null;
  private readonly modalDelete: HTMLElement | null;

  private menu: HTMLElement | null = null;
  private menuClip: PortalClip | null = null;
  private menuCard: HTMLElement | null = null;
  private modalClip: PortalClip | null = null;
  private modalCard: HTMLElement | null = null;
  private token: string | null;

  private socket: WebSocket | null = null;
  private reconnectTimer: number | null = null;
  private reconnectDelay = 1000;
  private hasConnected = false;

  constructor(private readonly root: HTMLElement) {
    this.apiBase = root.dataset.apiBase?.trim() || window.location.origin;
    this.gate = root.querySelector<HTMLElement>('[data-clips-login]');
    this.dashboard = root.querySelector<HTMLElement>('[data-clips-dashboard]');
    this.list = root.querySelector<HTMLElement>('[data-clips-list]');
    this.empty = root.querySelector<HTMLElement>('[data-clips-empty]');
    this.count = root.querySelector<HTMLElement>('[data-clips-count]');
    this.status = root.querySelector<HTMLElement>('[data-clips-status]');
    this.userLabel = root.querySelector<HTMLElement>('[data-clips-user]');

    this.modal = root.querySelector<HTMLDialogElement>('[data-clip-modal]');
    this.modalVideo = root.querySelector<HTMLVideoElement>('[data-clip-modal-video]');
    this.modalTitle = root.querySelector<HTMLElement>('[data-clip-modal-title]');
    this.modalScores = root.querySelector<HTMLElement>('[data-clip-modal-scores]');
    this.modalMeta = root.querySelector<HTMLElement>('[data-clip-modal-meta]');
    this.modalDownload = root.querySelector<HTMLElement>('[data-clip-modal-download]');
    this.modalDelete = root.querySelector<HTMLElement>('[data-clip-modal-delete]');
    this.token = null;
  }

  init(): void {
    this.consumeHandoff();
    this.token = this.readStoredToken();

    this.root.querySelector('[data-clips-logout]')?.addEventListener('click', () => this.logout());

    this.wireModal();
    this.wireGlobalDismiss();

    if (this.token) {
      this.showDashboard();
      void this.bootstrap();
    } else {
      this.showGate();
    }
  }

  // consumeHandoff reads `#token=` / `#error=` from the OAuth redirect, persists
  // the token, and strips the fragment so it is never left in history.
  private consumeHandoff(): void {
    const hash = window.location.hash.replace(/^#/, '');
    if (!hash) return;
    const params = new URLSearchParams(hash);
    const token = params.get('token');
    const error = params.get('error');
    if (token) {
      this.writeStoredToken(token);
    } else if (error) {
      this.setStatus(this.describeOAuthError(error), 'error');
    }
    if (token || error) {
      const url = window.location.pathname + window.location.search;
      window.history.replaceState(null, '', url);
    }
  }

  private async bootstrap(): Promise<void> {
    try {
      const user = await this.request<PortalUser>('/portal/me');
      if (this.userLabel) {
        this.userLabel.textContent = user.username?.trim() || 'Discord user';
      }
    } catch (error) {
      if (this.handleAuthError(error)) return;
    }
    await this.loadClips();
    // Live updates replace any manual refresh: the server pushes new and removed
    // clips over a WebSocket so the library stays current on its own.
    this.connectRealtime();
  }

  private async loadClips(): Promise<void> {
    if (!this.token) {
      this.showGate();
      return;
    }
    this.setStatus('Loading your clips...');
    try {
      const data = await this.request<ClipsListResponse>('/portal/clips');
      this.renderClips(data.clips || []);
      this.setStatus('');
    } catch (error) {
      if (this.handleAuthError(error)) return;
      this.setStatus(this.readableError(error), 'error');
    }
  }

  private renderClips(clips: PortalClip[]): void {
    if (!this.list) return;
    this.closeMenu();
    this.list.replaceChildren();
    if (this.count) this.count.textContent = String(clips.length);
    if (this.empty) this.empty.hidden = clips.length > 0;
    for (const clip of clips) {
      this.list.append(this.buildCard(clip));
    }
  }

  private buildCard(clip: PortalClip): HTMLElement {
    const card = document.createElement('article');
    card.className = 'clips-card';
    card.dataset.clipId = clip.id;

    // The whole card is the vertical thumbnail and is clickable to play.
    if (clip.thumbnail_url) {
      const img = document.createElement('img');
      img.className = 'clips-poster-img';
      img.src = clip.thumbnail_url;
      img.alt = '';
      img.loading = 'lazy';
      card.append(img);
    } else {
      const placeholder = document.createElement('div');
      placeholder.className = 'clips-poster-empty';
      placeholder.textContent = 'No preview';
      card.append(placeholder);
    }

    // Context menu trigger (top-left) — holds the download/delete actions.
    const menuBtn = document.createElement('button');
    menuBtn.type = 'button';
    menuBtn.className = 'clips-menu-btn';
    menuBtn.dataset.clipsMenuBtn = '';
    menuBtn.setAttribute('aria-label', 'Clip options');
    menuBtn.setAttribute('aria-haspopup', 'menu');
    menuBtn.setAttribute('aria-expanded', 'false');
    menuBtn.innerHTML = ICON_DOTS;
    menuBtn.addEventListener('click', (event) => {
      event.stopPropagation();
      this.toggleMenu(clip, card, menuBtn);
    });
    card.append(menuBtn);

    // Centre play affordance (a real button for keyboard users).
    const play = document.createElement('button');
    play.type = 'button';
    play.className = 'clips-play';
    play.setAttribute('aria-label', `Play ${clip.title?.trim() || 'clip'}`);
    play.innerHTML = ICON_PLAY;
    play.addEventListener('click', (event) => {
      event.stopPropagation();
      void this.openModal(clip, card);
    });
    card.append(play);

    const duration = formatDuration(clip.duration_seconds);
    if (duration) {
      const badge = document.createElement('span');
      badge.className = 'clips-duration';
      badge.textContent = duration;
      card.append(badge);
    }

    // Floating info over the lower third (non-interactive).
    const overlay = document.createElement('div');
    overlay.className = 'clips-overlay';

    const scores = this.buildScores(clip);
    if (scores) overlay.append(scores);

    const title = document.createElement('h3');
    title.className = 'clips-card-title';
    title.textContent = clip.title?.trim() || `Clip ${clip.rank || ''}`.trim();
    overlay.append(title);

    const meta = this.buildMeta(clip);
    if (meta) overlay.append(meta);

    card.append(overlay);

    // Clicking anywhere on the card (except the menu button) plays the clip.
    card.addEventListener('click', (event) => {
      if ((event.target as HTMLElement)?.closest('[data-clips-menu-btn]')) return;
      void this.openModal(clip, card);
    });

    return card;
  }

  // buildScores renders the AI scores as small glass pills, with the number
  // tinted by how strong each score is.
  private buildScores(clip: PortalClip): HTMLElement | null {
    const all: Array<[string, number]> = [
      ['Viral', clip.virality_score],
      ['Hook', clip.hook_score],
      ['Retention', clip.retention_score],
    ];
    const entries = all.filter(([, value]) => Number.isFinite(value) && value > 0);
    if (entries.length === 0) return null;
    const wrap = document.createElement('div');
    wrap.className = 'clips-scores';
    for (const [label, value] of entries) {
      const pill = document.createElement('span');
      pill.className = 'clips-score';
      pill.dataset.tone = value >= 8 ? 'good' : value < 5 ? 'low' : 'mid';
      const num = document.createElement('b');
      num.textContent = String(value);
      pill.append(`${label} `, num);
      wrap.append(pill);
    }
    return wrap;
  }

  // buildMeta renders the source video on one line and date · size beneath it,
  // keeping each line self-contained so a long source never orphans a divider.
  private buildMeta(clip: PortalClip): HTMLElement | null {
    const source = clip.video_title?.trim() || clip.video_uploader?.trim();
    const created = formatDate(clip.created_at);
    const size = formatBytes(clip.size_bytes);
    if (!source && !created && !size) return null;

    const wrap = document.createElement('div');
    wrap.className = 'clips-meta';

    if (source) {
      const src = document.createElement('p');
      src.className = 'clips-meta-src';
      src.textContent = source;
      wrap.append(src);
    }

    if (created || size) {
      const stats = document.createElement('div');
      stats.className = 'clips-meta-stats';
      if (created) stats.append(metaSpan(created));
      if (size) stats.append(metaSpan(size));
      wrap.append(stats);
    }

    return wrap;
  }

  // --- Context menu --------------------------------------------------------

  private ensureMenu(): HTMLElement {
    if (this.menu) return this.menu;
    const menu = document.createElement('div');
    menu.className = 'clips-menu';
    menu.setAttribute('role', 'menu');
    menu.hidden = true;

    const open = menuItem('Open', ICON_PLAY, false, () => {
      const clip = this.menuClip;
      const card = this.menuCard;
      this.closeMenu();
      if (clip && card) void this.openModal(clip, card);
    });
    const download = menuItem('Download', ICON_DOWNLOAD, false, () => {
      const clip = this.menuClip;
      const card = this.menuCard;
      this.closeMenu();
      if (clip && card) void this.downloadClip(clip, card);
    });
    const remove = menuItem('Delete', ICON_TRASH, true, () => {
      const clip = this.menuClip;
      const card = this.menuCard;
      this.closeMenu();
      if (clip && card) void this.removeClip(clip, card);
    });

    menu.append(open, download, remove);
    document.body.append(menu);
    this.menu = menu;
    return menu;
  }

  private toggleMenu(clip: PortalClip, card: HTMLElement, trigger: HTMLElement): void {
    const wasOpenForThis = !!this.menu && !this.menu.hidden && this.menuCard === card;
    // Always reset first so a previously-open trigger drops its expanded state.
    this.closeMenu();
    if (wasOpenForThis) return;

    const menu = this.ensureMenu();
    this.menuClip = clip;
    this.menuCard = card;

    // Show first so it can be measured, then position near the trigger.
    menu.hidden = false;
    const rect = trigger.getBoundingClientRect();
    const mw = menu.offsetWidth;
    const mh = menu.offsetHeight;
    let left = rect.left;
    let top = rect.bottom + 6;
    if (left + mw > window.innerWidth - 8) left = window.innerWidth - 8 - mw;
    if (top + mh > window.innerHeight - 8) top = rect.top - mh - 6;
    menu.style.left = `${Math.max(8, left)}px`;
    menu.style.top = `${Math.max(8, top)}px`;

    trigger.setAttribute('aria-expanded', 'true');
    this.menuTrigger = trigger;
  }

  private menuTrigger: HTMLElement | null = null;

  private closeMenu(): void {
    if (this.menu) this.menu.hidden = true;
    if (this.menuTrigger) {
      this.menuTrigger.setAttribute('aria-expanded', 'false');
      this.menuTrigger = null;
    }
    this.menuClip = null;
    this.menuCard = null;
  }

  private wireGlobalDismiss(): void {
    document.addEventListener('click', (event) => {
      if (!this.menu || this.menu.hidden) return;
      const target = event.target as HTMLElement | null;
      if (target?.closest('.clips-menu') || target?.closest('[data-clips-menu-btn]')) return;
      this.closeMenu();
    });
    document.addEventListener('keydown', (event) => {
      if (event.key === 'Escape') this.closeMenu();
    });
    // The clips panel scrolls, so close the menu on any scroll to avoid drift.
    window.addEventListener('scroll', () => this.closeMenu(), true);
    window.addEventListener('resize', () => this.closeMenu());
  }

  // --- Player modal --------------------------------------------------------

  private wireModal(): void {
    if (!this.modal) return;
    this.root.querySelector('[data-clip-modal-close]')?.addEventListener('click', () => this.closeModal());
    // Click on the backdrop (the dialog element itself) closes the player.
    this.modal.addEventListener('click', (event) => {
      if (event.target === this.modal) this.closeModal();
    });
    // Native Escape and programmatic close both fire 'close' — clean up there.
    this.modal.addEventListener('close', () => {
      this.stopModalVideo();
      this.modalClip = null;
      this.modalCard = null;
    });
    this.modalDownload?.addEventListener('click', () => {
      if (this.modalClip && this.modalCard) void this.downloadClip(this.modalClip, this.modalCard);
    });
    this.modalDelete?.addEventListener('click', () => {
      const clip = this.modalClip;
      const card = this.modalCard;
      if (!clip || !card) return;
      void this.removeClip(clip, card).then(() => {
        if (!card.isConnected) this.closeModal();
      });
    });
  }

  private async openModal(clip: PortalClip, card: HTMLElement): Promise<void> {
    this.closeMenu();
    if (!this.modal || !this.modalVideo) {
      // No player shell — fall back to a straight download.
      void this.downloadClip(clip, card);
      return;
    }
    this.modalClip = clip;
    this.modalCard = card;

    if (this.modalTitle) {
      this.modalTitle.textContent = clip.title?.trim() || `Clip ${clip.rank || ''}`.trim();
    }
    if (this.modalScores) {
      this.modalScores.replaceChildren();
      const scores = this.buildScores(clip);
      if (scores) this.modalScores.append(scores);
      this.modalScores.hidden = !scores;
    }
    if (this.modalMeta) {
      this.modalMeta.replaceChildren();
      const meta = this.buildMeta(clip);
      if (meta) this.modalMeta.append(meta);
      this.modalMeta.hidden = !meta;
    }

    // Reset the player and show the thumbnail until the stream URL resolves.
    this.modalVideo.removeAttribute('src');
    this.modalVideo.load();
    if (clip.thumbnail_url) this.modalVideo.poster = clip.thumbnail_url;
    else this.modalVideo.removeAttribute('poster');

    if (typeof this.modal.showModal === 'function') this.modal.showModal();
    else this.modal.setAttribute('open', '');

    this.setStatus('Loading clip...');
    try {
      const data = await this.request<ClipURLResponse>(`/portal/clips/${encodeURIComponent(clip.id)}/url`);
      // Ignore a stale response if the user already opened a different clip.
      if (this.modalClip !== clip) return;
      this.modalVideo.src = data.stream_url || data.url;
      this.modalVideo.load();
      void this.modalVideo.play().catch(() => {
        // Autoplay may be blocked; the native controls let the user start it.
      });
      this.setStatus('');
    } catch (error) {
      if (this.handleAuthError(error)) {
        this.closeModal();
        return;
      }
      this.setStatus(this.readableError(error), 'error');
    }
  }

  private closeModal(): void {
    if (!this.modal) return;
    if (this.modal.open && typeof this.modal.close === 'function') this.modal.close();
    else {
      this.modal.removeAttribute('open');
      this.stopModalVideo();
      this.modalClip = null;
      this.modalCard = null;
    }
  }

  private stopModalVideo(): void {
    if (!this.modalVideo) return;
    this.modalVideo.pause();
    this.modalVideo.removeAttribute('src');
    this.modalVideo.load();
  }

  // --- Actions -------------------------------------------------------------

  private async downloadClip(clip: PortalClip, card: HTMLElement): Promise<void> {
    card.dataset.busy = 'true';
    this.setStatus('Preparing download...');
    try {
      const data = await this.request<ClipURLResponse>(`/portal/clips/${encodeURIComponent(clip.id)}/url`);
      // The download URL carries Content-Disposition: attachment, so navigating
      // to it downloads the file in place without leaving the page.
      const anchor = document.createElement('a');
      anchor.href = data.url;
      anchor.download = data.filename || 'clip.mp4';
      anchor.rel = 'noopener';
      document.body.append(anchor);
      anchor.click();
      anchor.remove();
      this.setStatus('');
    } catch (error) {
      if (this.handleAuthError(error)) return;
      this.setStatus(this.readableError(error), 'error');
    } finally {
      delete card.dataset.busy;
    }
  }

  private async removeClip(clip: PortalClip, card: HTMLElement): Promise<void> {
    const label = clip.title?.trim() || 'this clip';
    if (!window.confirm(`Permanently delete ${label}? This also removes the file and breaks any shared link.`)) {
      return;
    }
    card.dataset.busy = 'true';
    this.setStatus('Removing clip...');
    try {
      await this.request(`/portal/clips/${encodeURIComponent(clip.id)}`, { method: 'DELETE' });
      card.remove();
      this.syncListCount();
      this.setStatus('Clip removed.');
    } catch (error) {
      if (this.handleAuthError(error)) return;
      this.setStatus(this.readableError(error), 'error');
      delete card.dataset.busy;
    }
  }

  // syncListCount reconciles the count badge and empty state with whatever cards
  // are currently in the grid — after a removal, an insertion, or a live update.
  private syncListCount(): void {
    if (!this.list) return;
    const remaining = this.list.childElementCount;
    if (this.count) this.count.textContent = String(remaining);
    if (this.empty) this.empty.hidden = remaining > 0;
  }

  // --- Realtime (WebSocket) ------------------------------------------------

  // connectRealtime opens the portal WebSocket so new and deleted clips stream
  // in without polling. It reconnects with backoff and resyncs the full list on
  // every reconnect so a dropped connection never leaves the grid stale.
  private connectRealtime(): void {
    if (!this.token) return;
    if (this.socket && (this.socket.readyState === WebSocket.OPEN || this.socket.readyState === WebSocket.CONNECTING)) {
      return;
    }
    let url: URL;
    try {
      url = new URL('/portal/clips/ws', this.apiBase);
    } catch {
      return;
    }
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
    url.searchParams.set('token', this.token);

    let socket: WebSocket;
    try {
      socket = new WebSocket(url.toString());
    } catch {
      this.scheduleReconnect();
      return;
    }
    this.socket = socket;

    socket.addEventListener('open', () => {
      this.reconnectDelay = 1000;
      // A reconnect may have missed events while we were offline — resync.
      if (this.hasConnected) void this.loadClips();
      this.hasConnected = true;
    });
    socket.addEventListener('message', (event) => this.handleRealtimeMessage(event));
    socket.addEventListener('close', () => {
      if (this.socket === socket) this.socket = null;
      if (this.token) this.scheduleReconnect();
    });
  }

  private scheduleReconnect(): void {
    if (this.reconnectTimer !== null || !this.token) return;
    const delay = this.reconnectDelay;
    this.reconnectDelay = Math.min(this.reconnectDelay * 2, 30000);
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = null;
      this.connectRealtime();
    }, delay);
  }

  private disconnectRealtime(): void {
    if (this.reconnectTimer !== null) {
      window.clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.reconnectDelay = 1000;
    this.hasConnected = false;
    const socket = this.socket;
    this.socket = null;
    // this.token is already cleared by logout(), so the close handler won't
    // schedule a reconnect.
    if (socket) {
      try {
        socket.close();
      } catch {
        // Ignore; the socket is being discarded anyway.
      }
    }
  }

  private handleRealtimeMessage(event: MessageEvent): void {
    if (typeof event.data !== 'string') return;
    let payload: { type?: string; clip?: PortalClip; id?: string };
    try {
      payload = JSON.parse(event.data);
    } catch {
      return;
    }
    if (!payload || typeof payload.type !== 'string') return;
    if (payload.type === 'clip.created' && payload.clip) {
      this.handleClipCreated(payload.clip);
    } else if (payload.type === 'clip.deleted' && typeof payload.id === 'string') {
      this.handleClipDeleted(payload.id);
    }
  }

  private handleClipCreated(clip: PortalClip): void {
    if (!this.list || !clip.id) return;
    // Ignore if we already hold this card (e.g. it raced with a resync).
    if (this.cardById(clip.id)) return;
    this.list.prepend(this.buildCard(clip)); // newest first, matching the list order
    this.syncListCount();
  }

  private handleClipDeleted(id: string): void {
    const card = this.cardById(id);
    if (!card) return;
    if (this.modalCard === card) this.closeModal();
    card.remove();
    this.syncListCount();
  }

  private cardById(id: string): HTMLElement | null {
    if (!this.list) return null;
    for (const child of Array.from(this.list.children)) {
      if ((child as HTMLElement).dataset.clipId === id) return child as HTMLElement;
    }
    return null;
  }

  private request<T>(path: string, init: RequestInit = {}): Promise<T> {
    if (!this.token) return Promise.reject(new Error('portal_unauthorized'));
    return requestJSON<T>(this.apiURL(path), {
      ...init,
      headers: {
        Authorization: `Bearer ${this.token}`,
        ...(init.headers || {}),
      },
    });
  }

  private apiURL(path: string): string {
    return new URL(path, this.apiBase).toString();
  }

  private handleAuthError(error: unknown): boolean {
    if (this.readableError(error) === 'portal_unauthorized') {
      this.logout();
      this.setStatus('Your session expired. Please sign in again.', 'error');
      return true;
    }
    return false;
  }

  private logout(): void {
    this.token = null;
    this.disconnectRealtime();
    this.closeMenu();
    this.closeModal();
    try {
      window.localStorage.removeItem(portalSessionKey);
    } catch {
      // Ignore storage failures; the in-memory token is already cleared.
    }
    this.showGate();
  }

  private showDashboard(): void {
    if (this.gate) this.gate.hidden = true;
    if (this.dashboard) this.dashboard.hidden = false;
  }

  private showGate(): void {
    if (this.dashboard) this.dashboard.hidden = true;
    if (this.gate) this.gate.hidden = false;
  }

  private setStatus(message: string, tone: StatusTone = 'neutral'): void {
    if (!this.status) return;
    this.status.textContent = message;
    this.status.dataset.tone = tone;
  }

  private readStoredToken(): string | null {
    try {
      const raw = window.localStorage.getItem(portalSessionKey);
      return raw && raw.trim() ? raw.trim() : null;
    } catch {
      return null;
    }
  }

  private writeStoredToken(token: string): void {
    this.token = token;
    try {
      window.localStorage.setItem(portalSessionKey, token);
    } catch {
      // The in-memory token still unlocks this tab.
    }
  }

  private describeOAuthError(code: string): string {
    switch (code) {
      case 'access_denied':
        return 'Discord sign-in was cancelled.';
      case 'invalid_state':
        return 'Sign-in expired. Please try again.';
      default:
        return 'Discord sign-in failed. Please try again.';
    }
  }

  private readableError(error: unknown): string {
    if (error instanceof Error) return error.message;
    return 'Something went wrong.';
  }
}

const metaSpan = (text: string): HTMLSpanElement => {
  const span = document.createElement('span');
  span.textContent = text;
  return span;
};

// menuItem builds a context-menu row: an icon glyph plus an uppercase label.
const menuItem = (label: string, icon: string, danger: boolean, onClick: () => void): HTMLButtonElement => {
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = danger ? 'clips-menu-item clips-menu-item--danger' : 'clips-menu-item';
  btn.setAttribute('role', 'menuitem');
  btn.innerHTML = `${icon}<span>${label}</span>`;
  btn.addEventListener('click', onClick);
  return btn;
};

// Static inline icons (no user data, so innerHTML is safe).
const ICON_DOTS =
  '<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><circle cx="12" cy="5" r="1.8"/><circle cx="12" cy="12" r="1.8"/><circle cx="12" cy="19" r="1.8"/></svg>';
const ICON_PLAY =
  '<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M8 5.5v13a1 1 0 0 0 1.52.85l10.5-6.5a1 1 0 0 0 0-1.7L9.52 4.65A1 1 0 0 0 8 5.5Z"/></svg>';
const ICON_DOWNLOAD =
  '<svg viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M12 3v11m0 0 4-4m-4 4-4-4" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/><path d="M5 16v2.5A1.5 1.5 0 0 0 6.5 20h11a1.5 1.5 0 0 0 1.5-1.5V16" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>';
const ICON_TRASH =
  '<svg viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M4 7h16M9 7V5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2m1 0-.7 11.1a2 2 0 0 1-2 1.9H8.7a2 2 0 0 1-2-1.9L6 7" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/></svg>';

const requestJSON = async <T>(url: string, init: RequestInit = {}): Promise<T> => {
  const response = await fetch(url, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(init.headers || {}),
    },
  });
  const data = await safeJSON(response);
  if (!response.ok) throw new Error(errorFromResponse(response, data));
  return data as T;
};

const safeJSON = async (response: Response): Promise<Record<string, unknown>> => {
  try {
    return (await response.json()) as Record<string, unknown>;
  } catch {
    return {};
  }
};

const errorFromResponse = (response: Response, data: Record<string, unknown>): string => {
  if (response.status === 401) return 'portal_unauthorized';
  const error = typeof data.error === 'string' ? data.error : '';
  if (error) return error;
  return `Request failed with status ${response.status}.`;
};

const formatDuration = (seconds: number): string => {
  if (!Number.isFinite(seconds) || seconds <= 0) return '';
  const total = Math.round(seconds);
  const mins = Math.floor(total / 60);
  const secs = total % 60;
  if (mins <= 0) return `${secs}s`;
  return `${mins}m ${secs.toString().padStart(2, '0')}s`;
};

const formatDate = (value: string): string => {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return date.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
};

const formatBytes = (bytes: number): string => {
  if (!Number.isFinite(bytes) || bytes <= 0) return '';
  const units = ['B', 'KB', 'MB', 'GB'];
  let size = bytes;
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024;
    unitIndex += 1;
  }
  const maximumFractionDigits = unitIndex === 0 ? 0 : 1;
  return `${new Intl.NumberFormat(undefined, { maximumFractionDigits }).format(size)} ${units[unitIndex]}`;
};
