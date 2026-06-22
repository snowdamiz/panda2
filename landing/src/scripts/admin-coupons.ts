import { getWallets } from '@wallet-standard/app';
import type { IdentifierString, Wallet, WalletAccount } from '@wallet-standard/base';
import { StandardConnect } from '@wallet-standard/features';
import { SolanaSignMessage } from '@solana/wallet-standard-features';
import { shortWalletAddress } from './account';
import { createWalletOption } from './wallet-options';

const adminSessionStorageKey = 'panda.adminWalletSession.v1';

type AdminCoupon = {
  coupon_id: string;
  code_prefix: string;
  plan: string;
  display_name: string;
  discount_lamports: number;
  max_redemptions: number;
  status: string;
  owner_note?: string;
  created_by_user_id: string;
  expires_at?: string;
  revoked_at?: string;
  pending: number;
  consumed: number;
  released: number;
  created_at: string;
  updated_at: string;
};

type AdminCouponListResponse = {
  coupons: AdminCoupon[];
  plan_lamports: Record<string, number>;
};

type AdminCouponCreateResponse = {
  coupon: AdminCoupon;
  code: string;
};

type AdminAuthChallenge = {
  challenge_id: string;
  message: string;
  expires_at: string;
  treasury_wallet: string;
};

type AdminSessionResponse = {
  session_token: string;
  wallet: string;
  expires_at: string;
};

type StoredAdminSession = {
  sessionToken: string;
  wallet: string;
  expiresAt: string;
};

type ConnectFeature = {
  connect(input?: { readonly silent?: boolean }): Promise<{ readonly accounts: readonly WalletAccount[] }>;
};

type SignMessageFeature = {
  signMessage(
    ...inputs: readonly {
      readonly account: WalletAccount;
      readonly message: Uint8Array;
    }[]
  ): Promise<readonly { readonly signedMessage: Uint8Array; readonly signature: Uint8Array; readonly signatureType?: 'ed25519' }[]>;
};

type AdminCouponNodes = {
  root: HTMLElement;
  login: HTMLElement;
  dashboard: HTMLElement;
  connectButton: HTMLButtonElement;
  walletDialog: HTMLDialogElement;
  walletCloseButton: HTMLButtonElement;
  walletList: HTMLElement;
  walletSummary: HTMLElement | null;
  couponForm: HTMLFormElement;
  planButtons: HTMLButtonElement[];
  discountInput: HTMLInputElement;
  couponCodeInput: HTMLInputElement;
  maxRedemptionsInput: HTMLInputElement;
  expiresInput: HTMLInputElement;
  noteInput: HTMLInputElement;
  fillDiscountButton: HTMLButtonElement;
  refreshButton: HTMLButtonElement;
  lockButton: HTMLButtonElement;
  count: HTMLElement;
  list: HTMLElement;
  created: HTMLElement;
  createdCode: HTMLElement;
  copyCodeButton: HTMLButtonElement;
  status: HTMLElement;
};

export const initAdminCoupons = () => {
  document.querySelectorAll<HTMLElement>('[data-admin-coupons]').forEach((root) => {
    const nodes = collectAdminCouponNodes(root);
    if (!nodes) return;
    new AdminCouponController(nodes).init();
  });
};

class AdminCouponController {
  private readonly nodes: AdminCouponNodes;
  private readonly walletRegistry = getWallets();
  private wallets: readonly Wallet[] = [];
  private session: StoredAdminSession | null = null;
  private planLamports: Record<string, number> = {};
  private selectedPlan = 'plus';

  constructor(nodes: AdminCouponNodes) {
    this.nodes = nodes;
  }

  init() {
    this.selectedPlan = this.nodes.planButtons.find((button) => button.classList.contains('active'))?.dataset.adminPlan || this.selectedPlan;
    this.session = this.readSession();
    this.renderWallets();
    this.renderSession();

    this.walletRegistry.on('register', () => this.renderWallets());
    this.walletRegistry.on('unregister', () => this.renderWallets());
    this.nodes.connectButton.addEventListener('click', () => this.openWalletDialog());
    this.nodes.walletCloseButton.addEventListener('click', () => this.closeWalletDialog());
    this.nodes.walletDialog.addEventListener('click', (event) => {
      if (event.target === this.nodes.walletDialog) this.closeWalletDialog();
    });

    this.nodes.planButtons.forEach((button) => {
      button.addEventListener('click', () => this.selectPlan(button.dataset.adminPlan || 'plus'));
    });

    this.nodes.fillDiscountButton.addEventListener('click', () => this.fillPlanDiscount());
    this.nodes.refreshButton.addEventListener('click', () => void this.loadCoupons());
    this.nodes.lockButton.addEventListener('click', () => this.logout());
    this.nodes.copyCodeButton.addEventListener('click', () => void this.copyCreatedCode());
    this.nodes.couponForm.addEventListener('submit', (event) => {
      event.preventDefault();
      void this.createCoupon();
    });
  }

  private renderWallets() {
    this.wallets = this.walletRegistry.get().filter(walletSupportsMessageSigning);
    this.nodes.walletList.replaceChildren();
    if (this.wallets.length === 0) {
      this.nodes.walletList.append(emptyWalletMessage());
      return;
    }

    this.wallets.forEach((wallet) => {
      const option = createWalletOption(wallet);
      option.addEventListener('click', () => void this.signInWithWallet(wallet));
      this.nodes.walletList.append(option);
    });
  }

  private renderSession() {
    const session = this.session;
    const ready = session !== null;
    this.nodes.login.hidden = ready;
    this.nodes.dashboard.hidden = !ready;
    if (!ready) {
      this.nodes.count.textContent = '--';
      if (this.nodes.walletSummary) this.nodes.walletSummary.textContent = '';
      this.nodes.list.replaceChildren();
      this.nodes.created.hidden = true;
      this.setStatus('');
      return;
    }
    if (this.nodes.walletSummary) {
      this.nodes.walletSummary.textContent = shortWalletAddress(session.wallet);
    }
    void this.loadCoupons();
  }

  private async signInWithWallet(wallet: Wallet) {
    this.setBusy(true);
    this.setStatus(`Opening ${wallet.name}.`);
    try {
      const connect = feature<ConnectFeature>(wallet, StandardConnect);
      const signMessage = feature<SignMessageFeature>(wallet, SolanaSignMessage);
      if (!connect || !signMessage) throw new Error(`${wallet.name} cannot sign Solana admin messages.`);
      const output = await connect.connect({ silent: false });
      const account = output.accounts.find(accountSupportsMessageSigning);
      if (!account) throw new Error(`${wallet.name} did not authorize a Solana signing account.`);

      this.setStatus('Requesting admin challenge.');
      const challenge = await this.requestPublic<AdminAuthChallenge>('/admin/auth/challenge', {
        method: 'POST',
        body: JSON.stringify({ wallet: account.address }),
      });
      const messageBytes = new TextEncoder().encode(challenge.message);
      this.setStatus('Sign the admin login message in your wallet.');
      const [signed] = await signMessage.signMessage({ account, message: messageBytes });
      if (!signed) throw new Error('Wallet did not return a message signature.');
      if (!bytesEqual(signed.signedMessage, messageBytes)) {
        throw new Error('Wallet changed the admin login message before signing.');
      }
      if (signed.signatureType && signed.signatureType !== 'ed25519') {
        throw new Error('Wallet returned a non-Ed25519 message signature.');
      }
      const session = await this.requestPublic<AdminSessionResponse>('/admin/auth/sessions', {
        method: 'POST',
        body: JSON.stringify({
          challenge_id: challenge.challenge_id,
          wallet: account.address,
          signature: bytesToBase64(signed.signature),
          signed_message: bytesToBase64(signed.signedMessage),
        }),
      });
      this.session = {
        sessionToken: session.session_token,
        wallet: session.wallet,
        expiresAt: session.expires_at,
      };
      this.saveSession(this.session);
      this.closeWalletDialog();
      this.renderSession();
      this.setStatus('Treasury wallet authenticated.');
    } catch (error) {
      const message = messageForError(readableError(error));
      this.setStatus(message, 'error');
    } finally {
      this.setBusy(false);
    }
  }

  private async loadCoupons() {
    if (!this.session) return;
    this.setBusy(true);
    this.setStatus('Loading coupons.');
    try {
      const response = await this.requestAdmin<AdminCouponListResponse>('/admin/coupons');
      this.planLamports = response.plan_lamports || {};
      this.renderCoupons(response.coupons || []);
      this.fillPlanDiscount(false);
      this.setStatus('Coupons loaded.');
    } catch (error) {
      this.handleRequestError(error);
    } finally {
      this.setBusy(false);
    }
  }

  private async createCoupon() {
    if (!this.session) return;
    if (!this.nodes.couponForm.reportValidity()) return;

    this.setBusy(true);
    this.setStatus('Creating coupon.');
    try {
      const body = {
        plan: this.selectedPlan,
        discount_lamports: integerValue(this.nodes.discountInput.value),
        coupon_code: this.nodes.couponCodeInput.value.trim(),
        max_redemptions: integerValue(this.nodes.maxRedemptionsInput.value),
        expires_at: this.nodes.expiresInput.value.trim(),
        note: this.nodes.noteInput.value.trim(),
      };
      const response = await this.requestAdmin<AdminCouponCreateResponse>('/admin/coupons', {
        method: 'POST',
        body: JSON.stringify(body),
      });
      this.renderCreatedCode(response.code);
      this.nodes.couponForm.reset();
      this.selectPlan(this.selectedPlan);
      await this.loadCoupons();
      this.setStatus(`Coupon ${response.coupon.coupon_id} created.`);
    } catch (error) {
      this.handleRequestError(error);
    } finally {
      this.setBusy(false);
    }
  }

  private async revokeCoupon(coupon: AdminCoupon) {
    if (!this.session || coupon.status === 'revoked') return;
    const confirmed = window.confirm(`Revoke coupon ${coupon.coupon_id}?`);
    if (!confirmed) return;
    this.setBusy(true);
    this.setStatus('Revoking coupon.');
    try {
      await this.requestAdmin<AdminCoupon>(`/admin/coupons/${encodeURIComponent(coupon.coupon_id)}/revoke`, {
        method: 'POST',
      });
      await this.loadCoupons();
      this.setStatus(`Coupon ${coupon.coupon_id} revoked.`);
    } catch (error) {
      this.handleRequestError(error);
    } finally {
      this.setBusy(false);
    }
  }

  private renderCoupons(coupons: AdminCoupon[]) {
    this.nodes.count.textContent = coupons.length === 1 ? '1 coupon' : `${coupons.length} coupons`;
    this.nodes.list.replaceChildren();
    if (coupons.length === 0) {
      const empty = document.createElement('p');
      empty.className = 'admin-empty';
      empty.textContent = 'No coupons yet.';
      this.nodes.list.append(empty);
      return;
    }
    coupons.forEach((coupon) => {
      const row = document.createElement('article');
      row.className = 'admin-coupon-row';
      row.dataset.status = coupon.status;

      const head = document.createElement('div');
      head.className = 'admin-coupon-row-head';

      const title = document.createElement('div');
      const id = document.createElement('strong');
      id.textContent = coupon.coupon_id;
      const prefix = document.createElement('span');
      prefix.textContent = `${coupon.code_prefix}...`;
      title.append(id, prefix);

      const status = document.createElement('em');
      status.textContent = formatStatus(coupon.status);
      head.append(title, status);

      const details = document.createElement('dl');
      details.className = 'admin-coupon-details';
      details.append(
        detail('Plan', coupon.display_name || formatStatus(coupon.plan)),
        detail('Discount', formatLamports(coupon.discount_lamports)),
        detail('Limit', coupon.max_redemptions > 0 ? String(coupon.max_redemptions) : 'Unlimited'),
        detail('Used', `${coupon.consumed} consumed / ${coupon.pending} pending`),
        detail('Expires', formatDate(coupon.expires_at)),
        detail('Note', coupon.owner_note || '--'),
      );

      const actions = document.createElement('div');
      actions.className = 'admin-coupon-actions';
      const revoke = document.createElement('button');
      revoke.type = 'button';
      revoke.textContent = coupon.status === 'revoked' ? 'Revoked' : 'Revoke';
      revoke.disabled = coupon.status === 'revoked';
      revoke.addEventListener('click', () => void this.revokeCoupon(coupon));
      actions.append(revoke);

      row.append(head, details, actions);
      this.nodes.list.append(row);
    });
  }

  private selectPlan(plan: string) {
    this.selectedPlan = plan;
    this.nodes.planButtons.forEach((button) => {
      const active = button.dataset.adminPlan === plan;
      button.classList.toggle('active', active);
      button.setAttribute('aria-pressed', String(active));
    });
  }

  private fillPlanDiscount(overwrite = true) {
    const lamports = this.planLamports[this.selectedPlan];
    if (!Number.isFinite(lamports) || lamports <= 0) {
      if (overwrite) this.setStatus('Plan lamports are not available from the API.', 'error');
      return;
    }
    if (!overwrite && this.nodes.discountInput.value.trim()) return;
    this.nodes.discountInput.value = String(lamports);
  }

  private renderCreatedCode(code: string) {
    this.nodes.created.hidden = false;
    this.nodes.createdCode.textContent = code;
  }

  private async copyCreatedCode() {
    const code = this.nodes.createdCode.textContent?.trim();
    if (!code) return;
    try {
      await navigator.clipboard.writeText(code);
      this.setStatus('Coupon code copied.');
    } catch {
      this.setStatus('Clipboard access was blocked; select the code manually.', 'error');
    }
  }

  private setBusy(busy: boolean) {
    this.nodes.root.classList.toggle('busy', busy);
    this.nodes.connectButton.disabled = busy;
    this.nodes.walletList.querySelectorAll<HTMLButtonElement>('button').forEach((button) => {
      button.disabled = busy;
    });
    this.nodes.dashboard.querySelectorAll<HTMLButtonElement>('button').forEach((button) => {
      if (button === this.nodes.lockButton) return;
      button.disabled = busy;
    });
  }

  private logout() {
    this.session = null;
    window.sessionStorage.removeItem(adminSessionStorageKey);
    this.renderSession();
    this.setStatus('Admin wallet logged out.');
  }

  private readSession(): StoredAdminSession | null {
    try {
      const raw = window.sessionStorage.getItem(adminSessionStorageKey);
      if (!raw) return null;
      const session = JSON.parse(raw) as Partial<StoredAdminSession>;
      if (!session.sessionToken || !session.wallet || !session.expiresAt) return null;
      if (new Date(session.expiresAt).getTime() <= Date.now()) return null;
      return {
        sessionToken: session.sessionToken,
        wallet: session.wallet,
        expiresAt: session.expiresAt,
      };
    } catch {
      return null;
    }
  }

  private saveSession(session: StoredAdminSession) {
    try {
      window.sessionStorage.setItem(adminSessionStorageKey, JSON.stringify(session));
    } catch {
      // The in-memory session still unlocks this tab.
    }
  }

  private async requestPublic<T>(path: string, init: RequestInit = {}): Promise<T> {
    return requestJSON<T>(this.apiURL(path), init);
  }

  private async requestAdmin<T>(path: string, init: RequestInit = {}): Promise<T> {
    if (!this.session) throw new Error('admin_unauthorized');
    return requestJSON<T>(this.apiURL(path), {
      ...init,
      headers: {
        Authorization: `Bearer ${this.session.sessionToken}`,
        ...(init.headers || {}),
      },
    });
  }

  private handleRequestError(error: unknown) {
    const message = readableError(error);
    if (message === 'admin_unauthorized') {
      this.logout();
      this.setStatus('Admin wallet session expired. Sign in again.', 'error');
      return;
    }
    this.setStatus(messageForError(message), 'error');
  }

  private openWalletDialog() {
    if (this.nodes.walletDialog.open) return;
    this.renderWallets();
    this.nodes.walletDialog.showModal();
  }

  private closeWalletDialog() {
    if (this.nodes.walletDialog.open) this.nodes.walletDialog.close();
  }

  private apiURL(path: string): string {
    const base = this.nodes.root.dataset.apiBase?.trim() || window.location.origin;
    return new URL(path, base).toString();
  }

  private setStatus(message: string, tone: 'neutral' | 'error' = 'neutral') {
    this.nodes.status.textContent = message;
    this.nodes.status.dataset.tone = tone;
  }
}

const collectAdminCouponNodes = (root: HTMLElement): AdminCouponNodes | null => {
  const login = root.querySelector<HTMLElement>('[data-admin-login]');
  const dashboard = root.querySelector<HTMLElement>('[data-admin-dashboard]');
  const connectButton = root.querySelector<HTMLButtonElement>('[data-admin-wallet-connect]');
  const walletDialog = root.querySelector<HTMLDialogElement>('[data-admin-wallet-dialog]');
  const walletCloseButton = root.querySelector<HTMLButtonElement>('[data-admin-wallet-close]');
  const walletList = root.querySelector<HTMLElement>('[data-admin-wallet-list]');
  const walletSummary = root.querySelector<HTMLElement>('[data-admin-wallet-summary]');
  const couponForm = root.querySelector<HTMLFormElement>('[data-admin-coupon-form]');
  const planButtons = Array.from(root.querySelectorAll<HTMLButtonElement>('[data-admin-plan]'));
  const discountInput = root.querySelector<HTMLInputElement>('[data-admin-discount]');
  const couponCodeInput = root.querySelector<HTMLInputElement>('[data-admin-coupon-code]');
  const maxRedemptionsInput = root.querySelector<HTMLInputElement>('[data-admin-max-redemptions]');
  const expiresInput = root.querySelector<HTMLInputElement>('[data-admin-expires]');
  const noteInput = root.querySelector<HTMLInputElement>('[data-admin-note]');
  const fillDiscountButton = root.querySelector<HTMLButtonElement>('[data-admin-fill-discount]');
  const refreshButton = root.querySelector<HTMLButtonElement>('[data-admin-refresh]');
  const lockButton = root.querySelector<HTMLButtonElement>('[data-admin-lock]');
  const count = root.querySelector<HTMLElement>('[data-admin-count]');
  const list = root.querySelector<HTMLElement>('[data-admin-coupon-list]');
  const created = root.querySelector<HTMLElement>('[data-admin-created]');
  const createdCode = root.querySelector<HTMLElement>('[data-admin-created-code]');
  const copyCodeButton = root.querySelector<HTMLButtonElement>('[data-admin-copy-code]');
  const status = root.querySelector<HTMLElement>('[data-admin-status]');
  if (
    !login ||
    !dashboard ||
    !connectButton ||
    !walletDialog ||
    !walletCloseButton ||
    !walletList ||
    !couponForm ||
    planButtons.length === 0 ||
    !discountInput ||
    !couponCodeInput ||
    !maxRedemptionsInput ||
    !expiresInput ||
    !noteInput ||
    !fillDiscountButton ||
    !refreshButton ||
    !lockButton ||
    !count ||
    !list ||
    !created ||
    !createdCode ||
    !copyCodeButton ||
    !status
  ) {
    return null;
  }
  return {
    root,
    login,
    dashboard,
    connectButton,
    walletDialog,
    walletCloseButton,
    walletList,
    walletSummary,
    couponForm,
    planButtons,
    discountInput,
    couponCodeInput,
    maxRedemptionsInput,
    expiresInput,
    noteInput,
    fillDiscountButton,
    refreshButton,
    lockButton,
    count,
    list,
    created,
    createdCode,
    copyCodeButton,
    status,
  };
};

const detail = (label: string, value: string): HTMLDivElement => {
  const wrapper = document.createElement('div');
  const term = document.createElement('dt');
  const description = document.createElement('dd');
  term.textContent = label;
  description.textContent = value;
  wrapper.append(term, description);
  return wrapper;
};

const walletSupportsMessageSigning = (wallet: Wallet): boolean => {
  return wallet.chains.some(isSolanaChain) &&
    Boolean(feature<ConnectFeature>(wallet, StandardConnect)) &&
    Boolean(feature<SignMessageFeature>(wallet, SolanaSignMessage));
};

const accountSupportsMessageSigning = (account: WalletAccount): boolean => {
  return account.chains.some(isSolanaChain) && account.features.includes(SolanaSignMessage);
};

const isSolanaChain = (chain: IdentifierString): boolean => String(chain).startsWith('solana:');

const feature = <T>(wallet: Wallet, name: IdentifierString): T | null => {
  const candidate = wallet.features[name];
  if (!candidate || typeof candidate !== 'object') return null;
  return candidate as T;
};

const emptyWalletMessage = (): HTMLParagraphElement => {
  const message = document.createElement('p');
  message.className = 'account-wallet-empty';
  message.textContent = 'No compatible Solana wallets were detected.';
  return message;
};

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
    return await response.json() as Record<string, unknown>;
  } catch {
    return {};
  }
};

const errorFromResponse = (response: Response, data: Record<string, unknown>): string => {
  const error = typeof data.error === 'string' ? data.error : '';
  return error || `Request failed with status ${response.status}.`;
};

const readableError = (error: unknown): string => {
  if (error instanceof Error && error.message) return error.message;
  return 'Something went wrong.';
};

const messageForError = (message: string): string => {
  const messages: Record<string, string> = {
    admin_wallet_not_configured: 'Set SOLANA_TREASURY_WALLET on the API before using admin.',
    admin_wallet_forbidden: 'Connect the configured treasury wallet to unlock admin.',
    admin_challenge_required: 'Wallet challenge was missing. Try signing in again.',
    admin_challenge_invalid: 'Wallet challenge expired. Try signing in again.',
    admin_signature_invalid: 'Wallet signature could not be verified.',
    admin_signed_message_mismatch: 'Wallet signed a different message. Try signing in again.',
    invalid_signature: 'Wallet returned an invalid signature.',
    invalid_signed_message: 'Wallet returned an invalid signed message.',
    invalid_wallet: 'The configured treasury wallet address is invalid.',
    unknown_plan: 'Choose a paid plan.',
    coupon_duplicate: 'That coupon code already exists.',
    coupon_expired: 'The expiration must be in the future.',
    coupon_revoked: 'That coupon is already revoked.',
    coupon_not_found: 'No coupon matched that id or prefix.',
    coupon_ambiguous: 'That prefix matches more than one coupon.',
    invalid_expiration: 'Use YYYY-MM-DD or RFC3339 for expiration.',
    bad_request: 'Coupon request was rejected.',
  };
  return messages[message] || message;
};

const integerValue = (value: string): number => {
  const parsed = Number.parseInt(value.trim(), 10);
  return Number.isFinite(parsed) ? parsed : 0;
};

const bytesToBase64 = (bytes: Uint8Array): string => {
  let binary = '';
  bytes.forEach((byte) => {
    binary += String.fromCharCode(byte);
  });
  return window.btoa(binary);
};

const bytesEqual = (left: Uint8Array, right: Uint8Array): boolean => {
  if (left.length !== right.length) return false;
  for (let index = 0; index < left.length; index += 1) {
    if (left[index] !== right[index]) return false;
  }
  return true;
};

const formatStatus = (value: string): string => {
  const normalized = value.trim();
  if (!normalized) return '--';
  return normalized
    .split(/[\s_-]+/)
    .filter(Boolean)
    .map((part) => `${part.charAt(0).toUpperCase()}${part.slice(1)}`)
    .join(' ');
};

const formatLamports = (value: number): string => {
  if (!Number.isFinite(value)) return '--';
  return `${new Intl.NumberFormat().format(value)} lamports`;
};

const formatDate = (value?: string): string => {
  if (!value) return 'Never';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return 'Never';
  return new Intl.DateTimeFormat(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  }).format(date);
};
