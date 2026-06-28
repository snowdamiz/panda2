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
    image_generations?: AccountUsageMetric;
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
  tabButtons: HTMLButtonElement[];
  tabPanels: HTMLElement[];
  billingLink: HTMLAnchorElement;
  refreshButton: HTMLButtonElement | null;
  logoutButton: HTMLButtonElement;
  deleteButton: HTMLButtonElement;
  walletDialog: HTMLDialogElement;
  walletCloseButton: HTMLButtonElement;
  status: HTMLElement;
  walletList: HTMLElement;
  walletDialogNote: HTMLElement | null;
};

type UsageUnit = 'count' | 'bytes';

const usageMeters: ReadonlyArray<{ key: string; metric: keyof AccountEntitlement['usage']; unit: UsageUnit }> = [
  { key: 'ai', metric: 'ai_responses', unit: 'count' },
  { key: 'search', metric: 'web_searches', unit: 'count' },
  { key: 'images', metric: 'image_generations', unit: 'count' },
  { key: 'storage', metric: 'knowledge_storage', unit: 'bytes' },
];

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
    this.nodes.tabButtons.forEach((button) => {
      button.addEventListener('click', () => this.selectTab(button.dataset.accountTabButton || 'overview'));
    });
    this.nodes.refreshButton?.addEventListener('click', () => this.refresh());
    this.nodes.logoutButton.addEventListener('click', () => this.logout());
    this.nodes.deleteButton.addEventListener('click', () => this.deleteAccount());
    this.nodes.walletCloseButton.addEventListener('click', () => this.closeWalletDialog());
    this.nodes.walletDialog.addEventListener('click', (event) => {
      if (event.target === this.nodes.walletDialog) this.closeWalletDialog();
    });
  }

  private selectTab(key: string) {
    this.nodes.tabButtons.forEach((button) => {
      const active = (button.dataset.accountTabButton || '') === key;
      button.classList.toggle('active', active);
      button.setAttribute('aria-selected', active ? 'true' : 'false');
    });
    this.nodes.tabPanels.forEach((panel) => {
      panel.hidden = (panel.dataset.accountTab || '') !== key;
    });
  }

  private refresh() {
    if (!readStoredWalletAccount()) return;
    this.setStatus('Refreshing account.');
    void this.refreshPaymentStatus();
    void this.refreshTrialStatus();
  }

  private setText(selector: string, value: string) {
    this.nodes.root.querySelectorAll<HTMLElement>(selector).forEach((node) => {
      node.textContent = value;
    });
  }

  private setTone(selector: string, key: string, value: string) {
    this.nodes.root.querySelectorAll<HTMLElement>(selector).forEach((node) => {
      node.dataset[key] = value;
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
      this.renderConnectButtonLabel();
      this.renderWalletIdentity(null);
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
    this.selectTab('overview');
    this.renderConnectButtonLabel();
    this.renderWalletIdentity(account);
    this.setStatus('Signed in.');
    void this.refreshPaymentStatus();
    void this.refreshTrialStatus();
  }

  private renderWalletIdentity(account: StoredWalletAccount | null) {
    if (!account) {
      this.setText('[data-account-wallet-summary]', '--');
      this.setText('[data-account-wallet-name]', '--');
      this.setText('[data-account-wallet-address]', '--');
      this.setText('[data-account-created]', '--');
      return;
    }
    this.setText('[data-account-wallet-summary]', `${account.walletName} · ${shortWalletAddress(account.walletAddress)}`);
    this.setText('[data-account-wallet-name]', account.walletName);
    this.setText('[data-account-wallet-address]', account.walletAddress);
    this.setText('[data-account-created]', formatDate(account.createdAt));
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
    let status: string;
    let tone: string;
    let timeLeft: string;
    if (isTrial && isUsable) {
      status = 'Trial active';
      tone = 'good';
      timeLeft = formatTimeLeft(entitlement.trial_ends_at || entitlement.period_end);
    } else if (isTrial) {
      status = 'Trial ended';
      tone = 'bad';
      timeLeft = 'Expired';
    } else {
      status = `${entitlement.display_name || formatStatus(entitlement.plan)} active`;
      tone = isUsable ? 'good' : 'bad';
      timeLeft = 'Paid plan';
    }
    this.setText('[data-account-trial-status]', status);
    this.setTone('[data-account-trial-status]', 'trialTone', tone);
    this.setText('[data-account-trial-time]', timeLeft);
    this.setText('[data-account-overview-pill]', status);
    this.setTone('[data-account-overview-pill]', 'tone', tone);

    for (const meter of usageMeters) {
      this.renderMeter(meter.key, entitlement.usage[meter.metric], meter.unit);
    }
  }

  private renderTrialUnavailable(status: string) {
    const tone = status === 'Trial unavailable' ? 'bad' : 'idle';
    this.setText('[data-account-trial-status]', status);
    this.setTone('[data-account-trial-status]', 'trialTone', tone);
    this.setText('[data-account-trial-time]', '--');
    this.setText('[data-account-overview-pill]', status);
    this.setTone('[data-account-overview-pill]', 'tone', tone);

    for (const meter of usageMeters) {
      this.renderMeter(meter.key, undefined, meter.unit);
    }
  }

  private renderMeter(key: string, metric: AccountUsageMetric | undefined, unit: UsageUnit) {
    this.setText(`[data-account-trial-${key}]`, formatRemaining(metric, unit));
    const fill = this.nodes.root.querySelector<HTMLElement>(`[data-account-meter-fill="${key}"]`);
    const hasLimit = Boolean(metric) && Number.isFinite(metric?.limit) && (metric?.limit ?? 0) > 0;
    if (!metric || !hasLimit) {
      this.setText(`[data-account-meter-detail="${key}"]`, '--');
      if (fill) {
        fill.style.width = '0%';
        fill.dataset.level = 'idle';
      }
      return;
    }
    const consumed = Math.max(0, (metric.used || 0) + (metric.reserved || 0));
    const ratio = Math.min(1, consumed / metric.limit);
    this.setText(`[data-account-meter-detail="${key}"]`, `${formatMetricValue(consumed, unit)} / ${formatMetricValue(metric.limit, unit)}`);
    if (fill) {
      fill.style.width = `${(ratio * 100).toFixed(1)}%`;
      fill.dataset.level = ratio >= 0.85 ? 'high' : 'normal';
    }
  }

  private renderPaymentOrder(order: StoredBillingOrder | null) {
    if (!order) {
      this.setText('[data-account-payment-status]', 'No payment yet');
      this.setTone('[data-account-payment-status]', 'paymentTone', 'idle');
      this.setText('[data-account-payment-plan]', '--');
      this.setText('[data-account-payment-amount]', '--');
      this.setText('[data-account-payment-expires]', '--');
      this.nodes.billingLink.href = billingURLForPlan(null);
      return;
    }

    this.setText('[data-account-payment-status]', formatStatus(order.status));
    this.setTone('[data-account-payment-status]', 'paymentTone', paymentTone(order.status));
    this.setText('[data-account-payment-plan]', order.display_name || formatStatus(order.plan));
    this.setText('[data-account-payment-amount]', order.amount_sol ? `${order.amount_sol} SOL` : '--');
    this.setText('[data-account-payment-expires]', formatDate(order.expires_at));
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
  const tabButtons = Array.from(root.querySelectorAll<HTMLButtonElement>('[data-account-tab-button]'));
  const tabPanels = Array.from(root.querySelectorAll<HTMLElement>('[data-account-tab]'));
  const loginView = root.querySelector<HTMLElement>('[data-account-login-view]');
  const dashboard = root.querySelector<HTMLElement>('[data-account-dashboard]');
  const billingLink = root.querySelector<HTMLAnchorElement>('[data-account-billing-link]');
  const refreshButton = root.querySelector<HTMLButtonElement>('[data-account-refresh]');
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
    tabButtons,
    tabPanels,
    billingLink,
    refreshButton,
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

const formatMetricValue = (value: number, unit: 'count' | 'bytes' = 'count'): string => {
  if (!Number.isFinite(value)) return '--';
  if (unit === 'bytes') return formatBytes(Math.max(0, value));
  return new Intl.NumberFormat().format(Math.max(0, value));
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
