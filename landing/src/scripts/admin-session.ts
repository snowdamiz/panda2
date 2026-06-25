import { getWallets } from '@wallet-standard/app';
import type { IdentifierString, Wallet, WalletAccount } from '@wallet-standard/base';
import { StandardConnect } from '@wallet-standard/features';
import { SolanaSignMessage } from '@solana/wallet-standard-features';

const adminSessionStorageKey = 'panda.adminWalletSession.v1';
export const adminSessionExpiredEvent = 'panda:admin-session-expired';

export type AdminSession = {
  sessionToken: string;
  wallet: string;
  expiresAt: string;
};

export type AdminRequest = <T>(path: string, init?: RequestInit) => Promise<T>;

export type AdminStatusTone = 'neutral' | 'error';

// AdminPanelContext is handed to each operator panel by the console so panels
// share one authenticated request path, status line, and busy state.
export type AdminPanelContext = {
  request: AdminRequest;
  apiBase: string;
  setStatus: (message: string, tone?: AdminStatusTone) => void;
  setBusy: (busy: boolean) => void;
};

export interface AdminPanel {
  init(): void;
  load(): Promise<void>;
  reset(): void;
}

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

// AdminSessionManager owns the treasury-wallet sign-in flow and the shared
// admin session used by every operator panel.
export class AdminSessionManager {
  private readonly apiBase: string;
  private readonly walletRegistry = getWallets();
  private current: AdminSession | null = null;

  constructor(apiBase: string) {
    this.apiBase = apiBase;
    this.current = this.readStoredSession();
  }

  get session(): AdminSession | null {
    return this.current;
  }

  isAuthenticated(): boolean {
    return this.current !== null;
  }

  signingWallets(): readonly Wallet[] {
    return this.walletRegistry.get().filter(walletSupportsMessageSigning);
  }

  onWalletsChanged(callback: () => void) {
    this.walletRegistry.on('register', callback);
    this.walletRegistry.on('unregister', callback);
  }

  async signInWithWallet(wallet: Wallet, onProgress?: (message: string) => void): Promise<AdminSession> {
    const progress = onProgress ?? (() => {});
    const connect = feature<ConnectFeature>(wallet, StandardConnect);
    const signMessage = feature<SignMessageFeature>(wallet, SolanaSignMessage);
    if (!connect || !signMessage) throw new Error(`${wallet.name} cannot sign Solana admin messages.`);

    progress(`Opening ${wallet.name}.`);
    const output = await connect.connect({ silent: false });
    const account = output.accounts.find(accountSupportsMessageSigning);
    if (!account) throw new Error(`${wallet.name} did not authorize a Solana signing account.`);

    progress('Requesting admin challenge.');
    const challenge = await this.requestPublic<AdminAuthChallenge>('/admin/auth/challenge', {
      method: 'POST',
      body: JSON.stringify({ wallet: account.address }),
    });
    const messageBytes = new TextEncoder().encode(challenge.message);
    progress('Sign the admin login message in your wallet.');
    const [signed] = await signMessage.signMessage({ account, message: messageBytes });
    if (!signed) throw new Error('Wallet did not return a message signature.');
    if (!bytesEqual(signed.signedMessage, messageBytes)) {
      throw new Error('Wallet changed the admin login message before signing.');
    }
    if (signed.signatureType && signed.signatureType !== 'ed25519') {
      throw new Error('Wallet returned a non-Ed25519 message signature.');
    }

    const response = await this.requestPublic<AdminSessionResponse>('/admin/auth/sessions', {
      method: 'POST',
      body: JSON.stringify({
        challenge_id: challenge.challenge_id,
        wallet: account.address,
        signature: bytesToBase64(signed.signature),
        signed_message: bytesToBase64(signed.signedMessage),
      }),
    });
    const session: AdminSession = {
      sessionToken: response.session_token,
      wallet: response.wallet,
      expiresAt: response.expires_at,
    };
    this.current = session;
    this.saveSession(session);
    return session;
  }

  logout() {
    this.current = null;
    try {
      window.sessionStorage.removeItem(adminSessionStorageKey);
    } catch {
      // The in-memory session is already cleared.
    }
  }

  requestPublic = <T>(path: string, init: RequestInit = {}): Promise<T> => {
    return requestJSON<T>(this.apiURL(path), init);
  };

  requestAdmin = async <T>(path: string, init: RequestInit = {}): Promise<T> => {
    if (!this.current) throw new Error('admin_unauthorized');
    try {
      return await requestJSON<T>(this.apiURL(path), {
        ...init,
        headers: {
          Authorization: `Bearer ${this.current.sessionToken}`,
          ...(init.headers || {}),
        },
      });
    } catch (error) {
      if (readableError(error) === 'admin_unauthorized') {
        this.logout();
        window.dispatchEvent(new CustomEvent(adminSessionExpiredEvent));
      }
      throw error;
    }
  };

  private apiURL(path: string): string {
    const base = this.apiBase.trim() || window.location.origin;
    return new URL(path, base).toString();
  }

  private readStoredSession(): AdminSession | null {
    try {
      const raw = window.sessionStorage.getItem(adminSessionStorageKey);
      if (!raw) return null;
      const session = JSON.parse(raw) as Partial<AdminSession>;
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

  private saveSession(session: AdminSession) {
    try {
      window.sessionStorage.setItem(adminSessionStorageKey, JSON.stringify(session));
    } catch {
      // The in-memory session still unlocks this tab.
    }
  }
}

export const walletSupportsMessageSigning = (wallet: Wallet): boolean => {
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

export const emptyWalletMessage = (): HTMLParagraphElement => {
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
  if (response.status === 401) return 'admin_unauthorized';
  const error = typeof data.error === 'string' ? data.error : '';
  return error || `Request failed with status ${response.status}.`;
};

export const readableError = (error: unknown): string => {
  if (error instanceof Error && error.message) return error.message;
  return 'Something went wrong.';
};

export const messageForError = (message: string): string => {
  const messages: Record<string, string> = {
    admin_unauthorized: 'Admin wallet session expired. Sign in again.',
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
    invalid_period_end: 'Use YYYY-MM-DD or RFC3339 for the renewal date.',
    invalid_trial_ends_at: 'Use YYYY-MM-DD or RFC3339 for the trial end date.',
    guild_id_required: 'A guild id is required.',
    guild_not_found: 'No guild matched that id.',
    guild_list_failed: 'Could not load guilds. Try again.',
    guild_billing_failed: 'Could not load guild billing. Try again.',
    guild_lookup_failed: 'Could not look up that guild. Try again.',
    subscription_update_failed: 'The subscription change was rejected.',
    bad_request: 'The request was rejected.',
  };
  return messages[message] || message;
};

export const formatStatus = (value: string): string => {
  const normalized = value.trim();
  if (!normalized) return '--';
  return normalized
    .split(/[\s_-]+/)
    .filter(Boolean)
    .map((part) => `${part.charAt(0).toUpperCase()}${part.slice(1)}`)
    .join(' ');
};

export const formatDate = (value?: string | null): string => {
  if (!value) return 'Never';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return 'Never';
  return new Intl.DateTimeFormat(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  }).format(date);
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
