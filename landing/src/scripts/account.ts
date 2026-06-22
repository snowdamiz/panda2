import { getWallets } from '@wallet-standard/app';
import type { IdentifierString, Wallet, WalletAccount } from '@wallet-standard/base';
import { StandardConnect } from '@wallet-standard/features';
import { createWalletOption } from './wallet-options';

const accountStorageKey = 'panda.walletAccount.v1';
const billingOrderStorageKey = 'panda.billingOrder.v1';
const guildAccountStorageKey = 'panda.guildAccount.v1';

export type StoredWalletAccount = {
  walletAddress: string;
  walletName: string;
  createdAt: string;
  updatedAt: string;
};

export type AccountBillingOrder = {
  order_id: string;
  plan?: string;
  display_name?: string;
  amount_sol?: string;
  status: string;
  expires_at?: string;
  updated_at?: string;
};

export type AccountUsageMetric = {
  metric: string;
  label: string;
  used: number;
  reserved: number;
  limit: number;
  remaining: number;
  formatted: string;
};

export type AccountEntitlement = {
  guild_id: string;
  plan: string;
  display_name: string;
  status: string;
  grace_state: string;
  payment_provider: string;
  period_start: string;
  period_end: string;
  trial_ends_at?: string;
  can_use_paid_features: boolean;
  read_only: boolean;
  usage: {
    ai_responses?: AccountUsageMetric;
    web_searches?: AccountUsageMetric;
    knowledge_storage?: AccountUsageMetric;
  };
};

export type StoredBillingOrder = {
  order_id: string;
  plan: string;
  display_name: string;
  amount_sol: string;
  status: string;
  expires_at: string;
  updatedAt: string;
};

export type StoredGuildAccount = {
  guildID: string;
  updatedAt: string;
};

type AccountNodes = {
  root: HTMLElement;
  connectButtons: HTMLButtonElement[];
  loginView: HTMLElement;
  dashboard: HTMLElement;
  headingKicker: HTMLElement | null;
  headingTitle: HTMLElement;
  headingCopy: HTMLElement;
  accountTitle: HTMLElement;
  paymentStatus: HTMLElement;
  paymentPlan: HTMLElement;
  paymentAmount: HTMLElement;
  paymentExpires: HTMLElement;
  trialStatus: HTMLElement;
  trialTime: HTMLElement;
  trialAI: HTMLElement;
  trialSearch: HTMLElement;
  trialStorage: HTMLElement;
  billingLink: HTMLAnchorElement;
  logoutButton: HTMLButtonElement;
  deleteButton: HTMLButtonElement;
  walletDialog: HTMLDialogElement;
  walletCloseButton: HTMLButtonElement;
  status: HTMLElement;
  walletList: HTMLElement;
  walletDialogNote: HTMLElement | null;
};

type ConnectFeature = {
  connect(input?: { readonly silent?: boolean }): Promise<{ readonly accounts: readonly WalletAccount[] }>;
};

export const readStoredWalletAccount = (): StoredWalletAccount | null => {
  try {
    const raw = window.localStorage.getItem(accountStorageKey);
    if (!raw) return null;
    const account = JSON.parse(raw) as Partial<StoredWalletAccount>;
    if (!account.walletAddress || !account.walletName) return null;
    return {
      walletAddress: account.walletAddress,
      walletName: account.walletName,
      createdAt: account.createdAt || new Date().toISOString(),
      updatedAt: account.updatedAt || new Date().toISOString(),
    };
  } catch {
    return null;
  }
};

export const readStoredBillingOrder = (): StoredBillingOrder | null => {
  try {
    const raw = window.localStorage.getItem(billingOrderStorageKey);
    if (!raw) return null;
    const order = JSON.parse(raw) as Partial<StoredBillingOrder>;
    if (!order.order_id || !order.status) return null;
    return {
      order_id: order.order_id,
      plan: order.plan || '',
      display_name: order.display_name || '',
      amount_sol: order.amount_sol || '',
      status: order.status,
      expires_at: order.expires_at || '',
      updatedAt: order.updatedAt || new Date().toISOString(),
    };
  } catch {
    return null;
  }
};

export const readStoredGuildAccount = (): StoredGuildAccount | null => {
  try {
    const raw = window.localStorage.getItem(guildAccountStorageKey);
    if (!raw) return null;
    const account = JSON.parse(raw) as Partial<StoredGuildAccount>;
    if (!account.guildID) return null;
    return {
      guildID: account.guildID,
      updatedAt: account.updatedAt || new Date().toISOString(),
    };
  } catch {
    return null;
  }
};

export const rememberBillingOrder = (order: AccountBillingOrder): StoredBillingOrder | null => {
  const orderID = stringValue(order.order_id);
  const status = stringValue(order.status);
  if (!orderID || !status) return null;

  const snapshot: StoredBillingOrder = {
    order_id: orderID,
    plan: stringValue(order.plan),
    display_name: stringValue(order.display_name),
    amount_sol: stringValue(order.amount_sol),
    status,
    expires_at: stringValue(order.expires_at),
    updatedAt: stringValue(order.updated_at) || new Date().toISOString(),
  };
  window.localStorage.setItem(billingOrderStorageKey, JSON.stringify(snapshot));
  return snapshot;
};

export const rememberGuildAccount = (guildID: string): StoredGuildAccount | null => {
  const normalized = stringValue(guildID);
  if (!normalized) return null;
  const snapshot: StoredGuildAccount = {
    guildID: normalized,
    updatedAt: new Date().toISOString(),
  };
  window.localStorage.setItem(guildAccountStorageKey, JSON.stringify(snapshot));
  return snapshot;
};

export const clearStoredAccount = () => {
  window.localStorage.removeItem(accountStorageKey);
  window.localStorage.removeItem(billingOrderStorageKey);
  window.localStorage.removeItem(guildAccountStorageKey);
};

export const clearStoredWalletAccount = () => {
  window.localStorage.removeItem(accountStorageKey);
};

export const billingURLForPlan = (plan: string | null): string => {
  const params = new URLSearchParams();
  if (plan) params.set('plan', plan);
  const query = params.toString();
  return query ? `/billing?${query}` : '/billing';
};

export const accountURLForPlan = (plan: string | null): string => {
  const params = new URLSearchParams();
  if (plan) params.set('plan', plan);
  const query = params.toString();
  return query ? `/account?${query}` : '/account';
};

export const shortWalletAddress = (address: string): string => {
  if (address.length <= 12) return address;
  return `${address.slice(0, 4)}...${address.slice(-4)}`;
};

export const initWalletAccount = () => {
  document.querySelectorAll<HTMLElement>('[data-wallet-account]').forEach((root) => {
    const nodes = collectAccountNodes(root);
    if (!nodes) return;
    new WalletAccountController(nodes).init();
  });
};

class WalletAccountController {
  private readonly nodes: AccountNodes;
  private readonly walletRegistry = getWallets();
  private wallets: readonly Wallet[] = [];
  private readonly intendedPlan = new URLSearchParams(window.location.search).get('plan');

  constructor(nodes: AccountNodes) {
    this.nodes = nodes;
  }

  init() {
    this.captureGuildFromURL();
    this.renderWallets();
    this.renderSavedSession();
    this.walletRegistry.on('register', () => this.renderWallets());
    this.walletRegistry.on('unregister', () => this.renderWallets());
    this.nodes.connectButtons.forEach((button) => {
      button.addEventListener('click', () => this.openWalletDialog());
    });
    this.nodes.logoutButton.addEventListener('click', () => this.logout());
    this.nodes.deleteButton.addEventListener('click', () => this.deleteAccount());
    this.nodes.walletCloseButton.addEventListener('click', () => this.closeWalletDialog());
    this.nodes.walletDialog.addEventListener('click', (event) => {
      if (event.target === this.nodes.walletDialog) this.closeWalletDialog();
    });
  }

  private renderWallets() {
    this.wallets = this.walletRegistry.get().filter(walletSupportsSolana);
    this.nodes.walletList.replaceChildren();
    if (this.wallets.length === 0) {
      this.setConnectButtonsDisabled(false);
      this.renderConnectButtonLabel();
      this.nodes.walletList.append(emptyWalletMessage());
      if (this.nodes.walletDialogNote) {
        this.nodes.walletDialogNote.textContent = 'Install or enable a Solana wallet in this browser.';
        this.nodes.walletDialogNote.dataset.tone = 'neutral';
      }
      return;
    }

    this.wallets.forEach((wallet, index) => {
      const option = createWalletOption(wallet);
      option.dataset.walletIndex = String(index);
      option.addEventListener('click', () => void this.connectWallet(wallet));
      this.nodes.walletList.append(option);
    });

    this.setConnectButtonsDisabled(false);
    this.renderConnectButtonLabel();
    if (this.nodes.walletDialogNote) {
      this.nodes.walletDialogNote.textContent = 'Select the wallet extension you want Panda to open.';
      this.nodes.walletDialogNote.dataset.tone = 'neutral';
    }
  }

  private async connectWallet(wallet: Wallet) {
    this.setBusy(true);
    this.setStatus(`Connecting ${wallet.name}.`);
    this.setDialogNote(`Opening ${wallet.name}.`);
    try {
      const connect = feature<ConnectFeature>(wallet, StandardConnect);
      if (!connect) throw new Error(`${wallet.name} does not expose wallet-standard connect.`);
      const output = await connect.connect({ silent: false });
      const account = output.accounts.find((candidate) => accountSupports(candidate));
      if (!account) throw new Error(`${wallet.name} did not authorize a Solana account.`);
      this.saveWalletSession(wallet, account);
    } catch (error) {
      const message = readableError(error);
      this.setStatus(message, 'error');
      this.setDialogNote(message, 'error');
    } finally {
      this.setBusy(false);
    }
  }

  private saveWalletSession(wallet: Wallet, walletAccount: WalletAccount) {
    const existing = readStoredWalletAccount();
    const now = new Date().toISOString();
    const account: StoredWalletAccount = {
      walletAddress: walletAccount.address,
      walletName: wallet.name,
      createdAt: existing?.createdAt || now,
      updatedAt: now,
    };
    window.localStorage.setItem(accountStorageKey, JSON.stringify(account));
    this.closeWalletDialog();
    if (this.intendedPlan) {
      this.setStatus('Signed in. Returning to checkout.');
      window.location.assign(billingURLForPlan(this.intendedPlan));
      return;
    }
    this.renderSavedSession();
    this.setStatus('Signed in.');
  }

  private renderSavedSession() {
    const account = readStoredWalletAccount();
    if (!account) {
      this.nodes.root.dataset.accountState = 'missing';
      this.nodes.loginView.hidden = false;
      this.nodes.dashboard.hidden = true;
      this.renderHeading(
        'Wallet login',
        'Sign in to checkout.',
        'Connect the Solana wallet you will use for payment. Panda attaches the purchased plan to Discord later through the activation key.',
      );
      this.nodes.accountTitle.textContent = 'Wallet sign in';
      this.renderConnectButtonLabel();
      this.renderPaymentOrder(null);
      this.renderTrialUnavailable('No server connected');
      return;
    }
    if (this.intendedPlan) {
      window.location.replace(billingURLForPlan(this.intendedPlan));
      return;
    }
    this.nodes.root.dataset.accountState = 'ready';
    this.nodes.loginView.hidden = true;
    this.nodes.dashboard.hidden = false;
    this.renderHeading(
      'Account',
      'Account dashboard.',
      'Review payment status and account controls.',
    );
    this.nodes.accountTitle.textContent = 'Wallet signed in';
    this.renderConnectButtonLabel();
    this.setStatus('Signed in.');
    void this.refreshPaymentStatus();
    void this.refreshTrialStatus();
  }

  private setBusy(busy: boolean) {
    this.nodes.root.classList.toggle('busy', busy);
    this.setConnectButtonsDisabled(busy);
    this.nodes.walletList.querySelectorAll<HTMLButtonElement>('[data-wallet-index]').forEach((option) => {
      option.disabled = busy;
    });
  }

  private openWalletDialog() {
    if (this.nodes.walletDialog.open) return;
    this.renderWallets();
    this.nodes.walletDialog.showModal();
  }

  private closeWalletDialog() {
    if (this.nodes.walletDialog.open) this.nodes.walletDialog.close();
  }

  private setStatus(message: string, tone: 'neutral' | 'error' = 'neutral') {
    this.nodes.status.textContent = message;
    this.nodes.status.dataset.tone = tone;
  }

  private renderConnectButtonLabel() {
    this.nodes.connectButtons.forEach((button) => {
      button.textContent = 'Sign in with wallet';
    });
  }

  private setDialogNote(message: string, tone: 'neutral' | 'error' = 'neutral') {
    if (!this.nodes.walletDialogNote) return;
    this.nodes.walletDialogNote.textContent = message;
    this.nodes.walletDialogNote.dataset.tone = tone;
  }

  private setConnectButtonsDisabled(disabled: boolean) {
    this.nodes.connectButtons.forEach((button) => {
      button.disabled = disabled;
    });
  }

  private renderHeading(kicker: string, title: string, copy: string) {
    if (this.nodes.headingKicker) this.nodes.headingKicker.textContent = kicker;
    this.nodes.headingTitle.textContent = title;
    this.nodes.headingCopy.textContent = copy;
  }

  private async refreshPaymentStatus() {
    const storedOrder = readStoredBillingOrder();
    this.renderPaymentOrder(storedOrder);
    if (!storedOrder) return;

    try {
      const order = await requestJSON<AccountBillingOrder>(this.apiURL(`/billing/sol/orders/${encodeURIComponent(storedOrder.order_id)}`));
      this.renderPaymentOrder(rememberBillingOrder(order));
      this.setStatus('Payment status refreshed.');
    } catch (error) {
      this.setStatus(`Could not refresh payment status: ${readableError(error)}`, 'error');
    }
  }

  private async refreshTrialStatus() {
    const guild = readStoredGuildAccount();
    if (!guild) {
      this.renderTrialUnavailable('No server connected');
      return;
    }

    this.renderTrialUnavailable('Loading trial');
    try {
      const entitlement = await requestJSON<AccountEntitlement>(this.apiURL(`/billing/entitlements/${encodeURIComponent(guild.guildID)}`));
      this.renderTrialEntitlement(entitlement);
    } catch (error) {
      const message = readableError(error);
      if (message === 'subscription_not_found') {
        this.renderTrialUnavailable('No trial found');
        return;
      }
      this.renderTrialUnavailable('Trial unavailable');
      this.setStatus(`Could not refresh trial status: ${message}`, 'error');
    }
  }

  private renderTrialEntitlement(entitlement: AccountEntitlement) {
    const isTrial = entitlement.plan === 'trial';
    const isUsable = entitlement.can_use_paid_features && !entitlement.read_only;
    if (isTrial && isUsable) {
      this.nodes.trialStatus.textContent = 'Trial active';
      this.nodes.trialStatus.dataset.trialTone = 'good';
      this.nodes.trialTime.textContent = formatTimeLeft(entitlement.trial_ends_at || entitlement.period_end);
    } else if (isTrial) {
      this.nodes.trialStatus.textContent = 'Trial ended';
      this.nodes.trialStatus.dataset.trialTone = 'bad';
      this.nodes.trialTime.textContent = 'Expired';
    } else {
      this.nodes.trialStatus.textContent = `${entitlement.display_name || formatStatus(entitlement.plan)} active`;
      this.nodes.trialStatus.dataset.trialTone = isUsable ? 'good' : 'bad';
      this.nodes.trialTime.textContent = 'Paid plan';
    }
    this.nodes.trialAI.textContent = formatRemaining(entitlement.usage.ai_responses);
    this.nodes.trialSearch.textContent = formatRemaining(entitlement.usage.web_searches);
    this.nodes.trialStorage.textContent = formatRemaining(entitlement.usage.knowledge_storage, 'bytes');
  }

  private renderTrialUnavailable(status: string) {
    this.nodes.trialStatus.textContent = status;
    this.nodes.trialStatus.dataset.trialTone = status === 'Trial unavailable' ? 'bad' : 'idle';
    this.nodes.trialTime.textContent = '--';
    this.nodes.trialAI.textContent = '--';
    this.nodes.trialSearch.textContent = '--';
    this.nodes.trialStorage.textContent = '--';
  }

  private renderPaymentOrder(order: StoredBillingOrder | null) {
    if (!order) {
      this.nodes.paymentStatus.textContent = 'No payment yet';
      this.nodes.paymentStatus.dataset.paymentTone = 'idle';
      this.nodes.paymentPlan.textContent = '--';
      this.nodes.paymentAmount.textContent = '--';
      this.nodes.paymentExpires.textContent = '--';
      this.nodes.billingLink.href = billingURLForPlan(null);
      return;
    }

    this.nodes.paymentStatus.textContent = formatStatus(order.status);
    this.nodes.paymentStatus.dataset.paymentTone = paymentTone(order.status);
    this.nodes.paymentPlan.textContent = order.display_name || formatStatus(order.plan);
    this.nodes.paymentAmount.textContent = order.amount_sol ? `${order.amount_sol} SOL` : '--';
    this.nodes.paymentExpires.textContent = formatDate(order.expires_at);
    this.nodes.billingLink.href = billingURLForPlan(order.plan || null);
  }

  private deleteAccount() {
    if (!readStoredWalletAccount()) return;
    const confirmed = window.confirm('Delete this Panda account from this browser?');
    if (!confirmed) return;
    clearStoredAccount();
    this.closeWalletDialog();
    this.renderSavedSession();
    this.setStatus('Account deleted.');
  }

  private logout() {
    if (!readStoredWalletAccount()) return;
    clearStoredWalletAccount();
    this.closeWalletDialog();
    this.renderSavedSession();
    this.setStatus('Logged out.');
  }

  private apiURL(path: string): string {
    const base = this.nodes.root.dataset.apiBase?.trim() || window.location.origin;
    return new URL(path, base).toString();
  }

  private captureGuildFromURL() {
    const guildID = new URLSearchParams(window.location.search).get('guild_id');
    if (guildID) rememberGuildAccount(guildID);
  }
}

const collectAccountNodes = (root: HTMLElement): AccountNodes | null => {
  const connectButtons = Array.from(root.querySelectorAll<HTMLButtonElement>('[data-account-connect]'));
  const loginView = root.querySelector<HTMLElement>('[data-account-login-view]');
  const dashboard = root.querySelector<HTMLElement>('[data-account-dashboard]');
  const headingKicker = root.querySelector<HTMLElement>('.account-heading .kicker');
  const headingTitle = root.querySelector<HTMLElement>('[data-account-heading-title]');
  const headingCopy = root.querySelector<HTMLElement>('[data-account-heading-copy]');
  const accountTitle = root.querySelector<HTMLElement>('[data-account-title]');
  const paymentStatus = root.querySelector<HTMLElement>('[data-account-payment-status]');
  const paymentPlan = root.querySelector<HTMLElement>('[data-account-payment-plan]');
  const paymentAmount = root.querySelector<HTMLElement>('[data-account-payment-amount]');
  const paymentExpires = root.querySelector<HTMLElement>('[data-account-payment-expires]');
  const trialStatus = root.querySelector<HTMLElement>('[data-account-trial-status]');
  const trialTime = root.querySelector<HTMLElement>('[data-account-trial-time]');
  const trialAI = root.querySelector<HTMLElement>('[data-account-trial-ai]');
  const trialSearch = root.querySelector<HTMLElement>('[data-account-trial-search]');
  const trialStorage = root.querySelector<HTMLElement>('[data-account-trial-storage]');
  const billingLink = root.querySelector<HTMLAnchorElement>('[data-account-billing-link]');
  const logoutButton = root.querySelector<HTMLButtonElement>('[data-account-logout]');
  const deleteButton = root.querySelector<HTMLButtonElement>('[data-account-delete]');
  const status = root.querySelector<HTMLElement>('[data-account-status]');
  const walletDialog = root.querySelector<HTMLDialogElement>('[data-account-wallet-dialog]');
  const walletCloseButton = root.querySelector<HTMLButtonElement>('[data-account-wallet-close]');
  const walletList = root.querySelector<HTMLElement>('[data-account-wallet-list]');
  const walletDialogNote = root.querySelector<HTMLElement>('[data-account-wallet-dialog-note]');
  if (
    connectButtons.length === 0 ||
    !loginView ||
    !dashboard ||
    !headingTitle ||
    !headingCopy ||
    !accountTitle ||
    !paymentStatus ||
    !paymentPlan ||
    !paymentAmount ||
    !paymentExpires ||
    !trialStatus ||
    !trialTime ||
    !trialAI ||
    !trialSearch ||
    !trialStorage ||
    !billingLink ||
    !logoutButton ||
    !deleteButton ||
    !status ||
    !walletDialog ||
    !walletCloseButton ||
    !walletList
  ) {
    return null;
  }
  return {
    root,
    connectButtons,
    loginView,
    dashboard,
    headingKicker,
    headingTitle,
    headingCopy,
    accountTitle,
    paymentStatus,
    paymentPlan,
    paymentAmount,
    paymentExpires,
    trialStatus,
    trialTime,
    trialAI,
    trialSearch,
    trialStorage,
    billingLink,
    logoutButton,
    deleteButton,
    walletDialog,
    walletCloseButton,
    status,
    walletList,
    walletDialogNote,
  };
};

const emptyWalletMessage = (): HTMLParagraphElement => {
  const message = document.createElement('p');
  message.className = 'account-wallet-empty';
  message.textContent = 'No Solana wallets were detected.';
  return message;
};

const walletSupportsSolana = (wallet: Wallet): boolean => {
  return wallet.chains.some(isSolanaChain) && Boolean(feature<ConnectFeature>(wallet, StandardConnect));
};

const accountSupports = (account: WalletAccount): boolean => {
  return account.chains.some(isSolanaChain);
};

const isSolanaChain = (chain: IdentifierString): boolean => String(chain).startsWith('solana:');

const feature = <T>(wallet: Wallet, name: IdentifierString): T | null => {
  const candidate = wallet.features[name];
  if (!candidate || typeof candidate !== 'object') return null;
  return candidate as T;
};

const readableError = (error: unknown): string => {
  if (error instanceof Error && error.message) return error.message;
  return 'Something went wrong.';
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

const stringValue = (value: unknown): string => typeof value === 'string' ? value.trim() : '';

const formatDate = (value: string): string => {
  if (!value) return '--';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '--';
  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  }).format(date);
};

const formatTimeLeft = (value: string): string => {
  if (!value) return '--';
  const endsAt = new Date(value).getTime();
  if (Number.isNaN(endsAt)) return '--';
  const remaining = endsAt - Date.now();
  if (remaining <= 0) return 'Expired';
  const minute = 60 * 1000;
  const hour = 60 * minute;
  const day = 24 * hour;
  if (remaining >= day) {
    const days = Math.ceil(remaining / day);
    return `${days} ${days === 1 ? 'day' : 'days'} left`;
  }
  if (remaining >= hour) {
    const hours = Math.ceil(remaining / hour);
    return `${hours} ${hours === 1 ? 'hour' : 'hours'} left`;
  }
  const minutes = Math.ceil(remaining / minute);
  return `${minutes} ${minutes === 1 ? 'minute' : 'minutes'} left`;
};

const formatRemaining = (metric: AccountUsageMetric | undefined, unit: 'count' | 'bytes' = 'count'): string => {
  if (!metric || !Number.isFinite(metric.remaining)) return '--';
  const remaining = Math.max(0, metric.remaining);
  if (unit === 'bytes') return `${formatBytes(remaining)} left`;
  return `${new Intl.NumberFormat().format(remaining)} left`;
};

const formatBytes = (value: number): string => {
  if (!Number.isFinite(value) || value <= 0) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let size = value;
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024;
    unitIndex += 1;
  }
  const maximumFractionDigits = unitIndex === 0 ? 0 : 1;
  return `${new Intl.NumberFormat(undefined, { maximumFractionDigits }).format(size)} ${units[unitIndex]}`;
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

const paymentTone = (status: string): string => {
  const normalized = status.trim().toLowerCase();
  if (normalized === 'verified' || normalized === 'activated') return 'good';
  if (normalized === 'failed' || normalized === 'expired') return 'bad';
  return 'idle';
};
