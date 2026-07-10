import { getWallets } from '@wallet-standard/app';
import type { IdentifierString, Wallet, WalletAccount } from '@wallet-standard/base';
import { StandardConnect } from '@wallet-standard/features';
import { SolanaSignTransaction } from '@solana/wallet-standard-features';
import { accountURLForPack, readStoredWalletAccount, rememberBillingOrder, shortWalletAddress } from './account';

type PaymentOrder = {
  order_id: string;
  pack: string;
  display_name: string;
  due_lamports: number;
  amount_sol: string;
  cluster: string;
  status: string;
  expires_at: string;
  submitted_transaction_signature?: string;
};

type VerificationResult = {
  order: PaymentOrder;
  verified: boolean;
  submitted_signature?: string;
  failure_code?: string;
  failure_error?: string;
};

type PaymentTransaction = {
  order: PaymentOrder;
  transaction: string;
  payer_wallet: string;
  last_valid_block_height: number;
};

type ActivationKeyReveal = {
  order: PaymentOrder;
  api_key: string;
  prefix: string;
  expires_at: string;
};

type ConnectFeature = {
  connect(input?: { readonly silent?: boolean }): Promise<{ readonly accounts: readonly WalletAccount[] }>;
};

type SignTransactionFeature = {
  readonly supportedTransactionVersions: readonly ('legacy' | 0)[];
  signTransaction(
    ...inputs: readonly {
      readonly account: WalletAccount;
      readonly transaction: Uint8Array;
      readonly chain?: string;
      readonly options?: { readonly preflightCommitment?: 'confirmed' | 'finalized' };
    }[]
  ): Promise<readonly { readonly signedTransaction: Uint8Array }[]>;
};

type PaymentNodes = {
  root: HTMLElement;
  form: HTMLFormElement;
  buyButton: HTMLButtonElement;
  couponInput: HTMLInputElement;
  result: HTMLElement;
  status: HTMLElement;
  keyBox: HTMLElement;
  key: HTMLElement;
  copyButton: HTMLButtonElement;
  packButtons: HTMLButtonElement[];
};

export const initSolPayments = () => {
  document.querySelectorAll<HTMLElement>('[data-sol-pay]').forEach((root) => {
    const nodes = collectNodes(root);
    if (!nodes) return;
    new SolPaymentController(nodes).init();
  });
};

class SolPaymentController {
  private readonly nodes: PaymentNodes;
  private readonly walletRegistry = getWallets();
  private selectedPack = 'plus';
  private wallets: readonly Wallet[] = [];
  private order: PaymentOrder | null = null;

  constructor(nodes: PaymentNodes) {
    this.nodes = nodes;
    const params = new URLSearchParams(window.location.search);
    const requestedPack = params.get('pack') || params.get('plan');
    const activePack = nodes.packButtons.find((button) => button.dataset.solPack === requestedPack) ||
      nodes.packButtons.find((button) => button.getAttribute('aria-pressed') === 'true');
    if (activePack) this.selectPack(activePack, false);
  }

  init() {
    this.renderWallets();
    this.applyAccountState();
    this.walletRegistry.on('register', () => this.renderWallets());
    this.walletRegistry.on('unregister', () => this.renderWallets());
    this.nodes.form.addEventListener('submit', (event) => {
      event.preventDefault();
      void this.buySelectedPack();
    });
    this.nodes.copyButton.addEventListener('click', () => void this.copyActivationKey());
    this.nodes.packButtons.forEach((button) => {
      button.addEventListener('click', () => this.selectPack(button));
    });
  }

  private selectPack(button: HTMLButtonElement, updateURL = true) {
    this.selectedPack = button.dataset.solPack || this.selectedPack;
    this.nodes.packButtons.forEach((item) => {
      const active = item === button;
      item.classList.toggle('active', active);
      item.setAttribute('aria-pressed', String(active));
    });
    const label = button.dataset.solPackLabel || button.querySelector<HTMLElement>('.checkout-plan-name')?.textContent?.trim() || this.selectedPack;
    this.renderBuyButtonLabel(label);
    if (updateURL) {
      const url = new URL(window.location.href);
      url.searchParams.set('pack', this.selectedPack);
      url.searchParams.delete('plan');
      window.history.replaceState({}, '', url);
    }
  }

  private renderWallets() {
    this.wallets = this.walletRegistry.get().filter((wallet) => walletSupportsSolana(wallet));
  }

  private applyAccountState() {
    const account = readStoredWalletAccount();
    this.nodes.root.dataset.accountState = account ? 'ready' : 'missing';
    this.renderBuyButtonLabel();
    this.setStatus('');
  }

  private async buySelectedPack() {
    const account = readStoredWalletAccount();
    if (!account) {
      this.setStatus('Opening wallet account login.');
      window.location.assign(accountURLForPack(this.selectedPack));
      return;
    }
    this.setBusy(true);
    this.nodes.keyBox.hidden = true;
    this.setStatus('Preparing checkout.');
    try {
      const order = await requestJSON<PaymentOrder>(this.apiURL('/billing/sol/orders'), {
        method: 'POST',
        body: JSON.stringify({
          pack: this.selectedPack,
          coupon_code: this.nodes.couponInput.value.trim(),
        }),
      });
      this.setOrder(order);
      if (this.isZeroDueOrder(order)) {
        await this.revealActivationKey();
        return;
      }
      this.setStatus('Approve the wallet prompt to finish payment.');
      const { wallet, walletAccount } = await this.connectAccountWallet(account.walletAddress, order.cluster);
      const prepared = await requestJSON<PaymentTransaction>(this.apiURL(`/billing/sol/orders/${order.order_id}/transaction`), {
        method: 'POST',
        body: JSON.stringify({ payer_wallet: walletAccount.address }),
      });
      this.setOrder(prepared.order);
      const signedTransaction = await this.signWithWallet(wallet, walletAccount, base64ToBytes(prepared.transaction), chainForCluster(prepared.order.cluster));
      this.setStatus('Submitting payment to Panda.');
      const result = await requestSubmission(
        this.apiURL(`/billing/sol/orders/${prepared.order.order_id}/submit`),
        bytesToBase64(signedTransaction),
      );
      this.setOrder({
        ...result.order,
        submitted_transaction_signature: result.submitted_signature || result.order.submitted_transaction_signature,
      });
      if (result.verified) {
        await this.revealActivationKey();
        return;
      }
      await this.pollVerification(result.submitted_signature || '');
    } catch (error) {
      this.setStatus(readableError(error), 'error');
    } finally {
      this.setBusy(false);
    }
  }

  private async connectAccountWallet(address: string, cluster: string): Promise<{ wallet: Wallet; walletAccount: WalletAccount }> {
    const chain = chainForCluster(cluster);
    const wallet = this.wallets.find((candidate) => walletSupportsSolana(candidate, chain));
    if (!wallet) {
      throw new Error('No compatible Solana wallet extension was found.');
    }
    const connect = feature<ConnectFeature>(wallet, StandardConnect);
    if (!connect) throw new Error(`${wallet.name} does not expose wallet-standard connect.`);
    const output = await connect.connect();
    const walletAccount = output.accounts.find((candidate) => candidate.address === address && accountSupports(candidate, chain));
    if (!walletAccount) {
      throw new Error(`Connect the account wallet ${shortWalletAddress(address)} to pay.`);
    }
    return { wallet, walletAccount };
  }

  private async signWithWallet(
    wallet: Wallet,
    account: WalletAccount,
    serialized: Uint8Array,
    chain: string,
  ): Promise<Uint8Array> {
    const signTransaction = feature<SignTransactionFeature>(wallet, SolanaSignTransaction);
    if (signTransaction && supportsLegacy(signTransaction.supportedTransactionVersions) && account.features.includes(SolanaSignTransaction)) {
      const [output] = await signTransaction.signTransaction({
        account,
        transaction: serialized,
        chain,
        options: { preflightCommitment: 'confirmed' },
      });
      if (!output) throw new Error('Wallet did not return a signed transaction.');
      return output.signedTransaction;
    }
    throw new Error(`${wallet.name} cannot sign SOL transfers from this page.`);
  }

  private async pollVerification(signature: string) {
    if (!this.order || !signature) {
      this.setStatus('Payment was submitted. Panda is waiting for confirmation.');
      return;
    }
    const delays = [2000, 4000, 8000, 16000, 30000];
    for (const wait of delays) {
      await delay(wait);
      const result = await requestVerification(this.apiURL(`/billing/sol/orders/${this.order.order_id}/verify`), signature);
      this.setOrder(result.order);
      if (result.verified) {
        await this.revealActivationKey();
        return;
      }
      if (result.failure_code !== 'pending_confirmation') {
        throw new Error(verificationMessage(result));
      }
    }
    this.setStatus('Payment submitted. Final confirmation is taking longer than usual; use Refresh payment status on your account page.');
  }

  private async revealActivationKey() {
    if (!this.order) return;
    const reveal = await requestJSON<ActivationKeyReveal>(this.apiURL(`/billing/sol/orders/${this.order.order_id}/activation-key`), {
      method: 'POST',
    });
    this.setOrder(reveal.order);
    this.nodes.key.textContent = reveal.api_key;
    this.nodes.keyBox.hidden = false;
    this.setStatus('Payment verified. Activation key ready.');
  }

  private async copyActivationKey() {
    const value = this.nodes.key.textContent?.trim();
    if (!value) return;
    try {
      await navigator.clipboard.writeText(value);
      this.setStatus('Activation key copied.');
    } catch {
      this.setStatus('Clipboard access was blocked; select the key manually.', 'error');
    }
  }

  private setBusy(busy: boolean) {
    this.nodes.root.classList.toggle('busy', busy);
    this.nodes.buyButton.disabled = busy;
    this.nodes.couponInput.disabled = busy;
    this.nodes.packButtons.forEach((button) => {
      button.disabled = busy;
    });
  }

  private setStatus(message: string, tone: 'neutral' | 'error' = 'neutral') {
    const trimmed = message.trim();
    this.nodes.status.textContent = trimmed;
    this.nodes.status.dataset.tone = tone;
    this.nodes.result.hidden = trimmed === '' && this.nodes.keyBox.hidden;
  }

  private setOrder(order: PaymentOrder) {
    this.order = order;
    rememberBillingOrder(order);
  }

  private renderBuyButtonLabel(label?: string) {
    const activeButton = this.nodes.packButtons.find((button) => button.getAttribute('aria-pressed') === 'true');
    const packLabel = label || activeButton?.dataset.solPackLabel || activeButton?.querySelector<HTMLElement>('.checkout-plan-name')?.textContent?.trim() || this.selectedPack;
    const account = readStoredWalletAccount();
    this.nodes.buyButton.textContent = account ? `Buy ${packLabel}` : 'Log in to buy';
  }

  private apiURL(path: string): string {
    const base = this.nodes.root.dataset.apiBase?.trim() || window.location.origin;
    return new URL(path, base).toString();
  }

  private isZeroDueOrder(order: PaymentOrder | null): boolean {
    return Boolean(order && order.due_lamports === 0);
  }
}

const collectNodes = (root: HTMLElement): PaymentNodes | null => {
  const form = root.querySelector<HTMLFormElement>('[data-sol-form]');
  const buyButton = root.querySelector<HTMLButtonElement>('[data-sol-buy]');
  const couponInput = root.querySelector<HTMLInputElement>('[data-sol-coupon]');
  const result = root.querySelector<HTMLElement>('[data-sol-result]');
  const status = root.querySelector<HTMLElement>('[data-sol-status]');
  const keyBox = root.querySelector<HTMLElement>('[data-sol-key-box]');
  const key = root.querySelector<HTMLElement>('[data-sol-key]');
  const copyButton = root.querySelector<HTMLButtonElement>('[data-sol-copy]');
  const packButtons = Array.from(root.querySelectorAll<HTMLButtonElement>('[data-sol-pack]'));
  if (!form || !buyButton || !couponInput || !result || !status || !keyBox || !key || !copyButton || packButtons.length === 0) {
    return null;
  }
  return {
    root,
    form,
    buyButton,
    couponInput,
    result,
    status,
    keyBox,
    key,
    copyButton,
    packButtons,
  };
};

const walletSupportsSolana = (wallet: Wallet, chain?: IdentifierString): boolean => {
  const signTransaction = feature<SignTransactionFeature>(wallet, SolanaSignTransaction);
  const supportsChain = chain ? wallet.chains.includes(chain) : wallet.chains.some((candidate) => String(candidate).startsWith('solana:'));
  return supportsChain &&
    Boolean(feature<ConnectFeature>(wallet, StandardConnect)) &&
    Boolean(signTransaction && supportsLegacy(signTransaction.supportedTransactionVersions));
};

const accountSupports = (account: WalletAccount, chain: IdentifierString): boolean => {
  return account.chains.includes(chain) && account.features.includes(SolanaSignTransaction);
};

const feature = <T>(wallet: Wallet, name: IdentifierString): T | null => {
  const candidate = wallet.features[name];
  if (!candidate || typeof candidate !== 'object') return null;
  return candidate as T;
};

const supportsLegacy = (versions: readonly ('legacy' | 0)[]): boolean => versions.includes('legacy');

const chainForCluster = (cluster: string): IdentifierString => {
  const normalized = cluster.trim().toLowerCase();
  if (normalized === 'mainnet' || normalized === 'mainnet-beta') return 'solana:mainnet';
  if (normalized === 'testnet') return 'solana:testnet';
  return 'solana:devnet';
};

const requestJSON = async <T>(url: string, init: RequestInit): Promise<T> => {
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

const requestSubmission = async (url: string, signedTransaction: string): Promise<VerificationResult> => {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ signed_transaction: signedTransaction }),
  });
  const data = await safeJSON(response);
  if ('order' in data && typeof data.order === 'object') return data as VerificationResult;
  if (!response.ok) throw new Error(errorFromResponse(response, data));
  throw new Error('Unexpected transaction submission response.');
};

const requestVerification = async (url: string, signature: string): Promise<VerificationResult> => {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ signature }),
  });
  const data = await safeJSON(response);
  if ('order' in data && typeof data.order === 'object') return data as VerificationResult;
  if (!response.ok) throw new Error(errorFromResponse(response, data));
  throw new Error('Unexpected verification response.');
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

const verificationMessage = (result: VerificationResult): string => {
  if (result.failure_code === 'pending_confirmation') {
    return 'Payment submitted. Panda is waiting for confirmation.';
  }
  if (result.failure_error) return result.failure_error;
  return result.failure_code || 'Payment could not be verified.';
};

const readableError = (error: unknown): string => {
  if (error instanceof Error && error.message) return error.message;
  return 'Something went wrong.';
};

const base64ToBytes = (value: string): Uint8Array => {
  const binary = window.atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes;
};

const bytesToBase64 = (bytes: Uint8Array): string => {
  let binary = '';
  bytes.forEach((byte) => {
    binary += String.fromCharCode(byte);
  });
  return window.btoa(binary);
};

const delay = (ms: number): Promise<void> => new Promise((resolve) => {
  window.setTimeout(resolve, ms);
});
