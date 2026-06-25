import type { Wallet } from '@wallet-standard/base';
import { shortWalletAddress } from './account';
import { createWalletOption } from './wallet-options';
import { CouponsPanel } from './admin-coupons';
import { GuildsPanel } from './admin-guilds';
import {
  AdminSessionManager,
  adminSessionExpiredEvent,
  emptyWalletMessage,
  messageForError,
  readableError,
  type AdminPanel,
  type AdminPanelContext,
  type AdminStatusTone,
} from './admin-session';

type ConsoleNodes = {
  root: HTMLElement;
  login: HTMLElement;
  dashboard: HTMLElement;
  connectButton: HTMLButtonElement;
  walletDialog: HTMLDialogElement;
  walletCloseButton: HTMLButtonElement;
  walletList: HTMLElement;
  walletSummary: HTMLElement | null;
  refreshButton: HTMLButtonElement;
  lockButton: HTMLButtonElement;
  tabButtons: HTMLButtonElement[];
  tabPanels: HTMLElement[];
  status: HTMLElement;
};

export const initAdminConsole = () => {
  document.querySelectorAll<HTMLElement>('[data-admin-root]').forEach((root) => {
    const nodes = collectConsoleNodes(root);
    if (!nodes) return;
    new AdminConsole(nodes).init();
  });
};

class AdminConsole {
  private readonly nodes: ConsoleNodes;
  private readonly session: AdminSessionManager;
  private readonly panels = new Map<string, AdminPanel>();
  private readonly loaded = new Set<string>();
  private activeTab = '';

  constructor(nodes: ConsoleNodes) {
    this.nodes = nodes;
    this.session = new AdminSessionManager(nodes.root.dataset.apiBase?.trim() || window.location.origin);
  }

  init() {
    const ctx: AdminPanelContext = {
      request: this.session.requestAdmin,
      apiBase: this.nodes.root.dataset.apiBase?.trim() || window.location.origin,
      setStatus: (message, tone) => this.setStatus(message, tone),
      setBusy: (busy) => this.setBusy(busy),
    };

    this.nodes.tabPanels.forEach((panel) => {
      const key = panel.dataset.adminTab;
      if (!key) return;
      const instance = key === 'guilds'
        ? GuildsPanel.fromRoot(panel, ctx)
        : key === 'coupons'
          ? CouponsPanel.fromRoot(panel, ctx)
          : null;
      if (!instance) return;
      instance.init();
      this.panels.set(key, instance);
    });

    this.activeTab = this.nodes.tabButtons.find((button) => button.classList.contains('active'))?.dataset.adminTabButton
      || this.nodes.tabButtons[0]?.dataset.adminTabButton
      || 'guilds';

    this.renderWallets();
    this.session.onWalletsChanged(() => this.renderWallets());

    this.nodes.connectButton.addEventListener('click', () => this.openWalletDialog());
    this.nodes.walletCloseButton.addEventListener('click', () => this.closeWalletDialog());
    this.nodes.walletDialog.addEventListener('click', (event) => {
      if (event.target === this.nodes.walletDialog) this.closeWalletDialog();
    });
    this.nodes.refreshButton.addEventListener('click', () => void this.refreshActive());
    this.nodes.lockButton.addEventListener('click', () => this.logout());
    this.nodes.tabButtons.forEach((button) => {
      button.addEventListener('click', () => this.selectTab(button.dataset.adminTabButton || 'guilds'));
    });
    window.addEventListener(adminSessionExpiredEvent, () => this.handleExpiredSession());

    this.renderSession();
  }

  private renderWallets() {
    const wallets = this.session.signingWallets();
    this.nodes.walletList.replaceChildren();
    if (wallets.length === 0) {
      this.nodes.walletList.append(emptyWalletMessage());
      return;
    }
    wallets.forEach((wallet) => {
      const option = createWalletOption(wallet);
      option.addEventListener('click', () => void this.signIn(wallet));
      this.nodes.walletList.append(option);
    });
  }

  private renderSession() {
    const ready = this.session.isAuthenticated();
    this.nodes.login.hidden = ready;
    this.nodes.dashboard.hidden = !ready;
    if (!ready) {
      this.loaded.clear();
      this.panels.forEach((panel) => panel.reset());
      if (this.nodes.walletSummary) this.nodes.walletSummary.textContent = '';
      this.setStatus('');
      return;
    }
    if (this.nodes.walletSummary && this.session.session) {
      this.nodes.walletSummary.textContent = shortWalletAddress(this.session.session.wallet);
    }
    this.activateTab(this.activeTab, true);
  }

  private async signIn(wallet: Wallet) {
    this.setBusy(true);
    try {
      await this.session.signInWithWallet(wallet, (message) => this.setStatus(message));
      this.closeWalletDialog();
      this.renderSession();
      this.setStatus('Treasury wallet authenticated.');
    } catch (error) {
      this.setStatus(messageForError(readableError(error)), 'error');
    } finally {
      this.setBusy(false);
    }
  }

  private selectTab(key: string) {
    if (!this.panels.has(key)) return;
    this.activeTab = key;
    this.activateTab(key, false);
  }

  private activateTab(key: string, forceLoad: boolean) {
    this.nodes.tabButtons.forEach((button) => {
      const active = button.dataset.adminTabButton === key;
      button.classList.toggle('active', active);
      button.setAttribute('aria-selected', String(active));
    });
    this.nodes.tabPanels.forEach((panel) => {
      panel.hidden = panel.dataset.adminTab !== key;
    });
    if (forceLoad || !this.loaded.has(key)) {
      this.loaded.add(key);
      void this.panels.get(key)?.load();
    }
  }

  private async refreshActive() {
    if (!this.session.isAuthenticated()) return;
    await this.panels.get(this.activeTab)?.load();
  }

  private logout() {
    this.session.logout();
    this.renderSession();
    this.setStatus('Admin wallet logged out.');
  }

  private handleExpiredSession() {
    this.setBusy(false);
    this.renderSession();
    this.setStatus(messageForError('admin_unauthorized'), 'error');
  }

  private openWalletDialog() {
    if (this.nodes.walletDialog.open) return;
    this.renderWallets();
    this.nodes.walletDialog.showModal();
  }

  private closeWalletDialog() {
    if (this.nodes.walletDialog.open) this.nodes.walletDialog.close();
  }

  private setBusy(busy: boolean) {
    this.nodes.root.classList.toggle('busy', busy);
    this.nodes.connectButton.disabled = busy;
    this.nodes.walletList.querySelectorAll<HTMLButtonElement>('button').forEach((button) => {
      button.disabled = busy;
    });
    this.nodes.dashboard.querySelectorAll<HTMLButtonElement | HTMLInputElement | HTMLSelectElement>('button, input, select').forEach((control) => {
      if (control === this.nodes.lockButton) return;
      if (this.nodes.tabButtons.includes(control as HTMLButtonElement)) return;
      control.disabled = busy;
    });
  }

  private setStatus(message: string, tone: AdminStatusTone = 'neutral') {
    this.nodes.status.textContent = message;
    this.nodes.status.dataset.tone = tone;
  }
}

const collectConsoleNodes = (root: HTMLElement): ConsoleNodes | null => {
  const login = root.querySelector<HTMLElement>('[data-admin-login]');
  const dashboard = root.querySelector<HTMLElement>('[data-admin-dashboard]');
  const connectButton = root.querySelector<HTMLButtonElement>('[data-admin-wallet-connect]');
  const walletDialog = root.querySelector<HTMLDialogElement>('[data-admin-wallet-dialog]');
  const walletCloseButton = root.querySelector<HTMLButtonElement>('[data-admin-wallet-close]');
  const walletList = root.querySelector<HTMLElement>('[data-admin-wallet-list]');
  const walletSummary = root.querySelector<HTMLElement>('[data-admin-wallet-summary]');
  const refreshButton = root.querySelector<HTMLButtonElement>('[data-admin-refresh]');
  const lockButton = root.querySelector<HTMLButtonElement>('[data-admin-lock]');
  const tabButtons = Array.from(root.querySelectorAll<HTMLButtonElement>('[data-admin-tab-button]'));
  const tabPanels = Array.from(root.querySelectorAll<HTMLElement>('[data-admin-tab]'));
  const status = root.querySelector<HTMLElement>('[data-admin-status]');
  if (
    !login ||
    !dashboard ||
    !connectButton ||
    !walletDialog ||
    !walletCloseButton ||
    !walletList ||
    !refreshButton ||
    !lockButton ||
    tabButtons.length === 0 ||
    tabPanels.length === 0 ||
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
    refreshButton,
    lockButton,
    tabButtons,
    tabPanels,
    status,
  };
};
