import { getWallets } from '@wallet-standard/app';
import type { IdentifierString, Wallet, WalletAccount } from '@wallet-standard/base';
import { StandardConnect } from '@wallet-standard/features';
import { SolanaSignTransaction } from '@solana/wallet-standard-features';

type PaymentOrder = {
  order_id: string;
  guild_id: string;
  billing_owner_user_id?: string;
  plan: string;
  display_name: string;
  list_lamports: number;
  discount_lamports: number;
  due_lamports: number;
  expected_lamports: number;
  amount_sol: string;
  coupon_prefix?: string;
  destination_wallet: string;
  reference: string;
  memo: string;
  cluster: string;
  confirmation_threshold: 'confirmed' | 'finalized';
  status: string;
  payment_url: string;
  verified_transaction_signature?: string;
  expires_at: string;
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
  walletSelect: HTMLSelectElement;
  connectButton: HTMLButtonElement;
  sendButton: HTMLButtonElement;
  verifyButton: HTMLButtonElement;
  revealButton: HTMLButtonElement;
  copyButton: HTMLButtonElement;
  guildInput: HTMLInputElement;
  ownerInput: HTMLInputElement;
  emailInput: HTMLInputElement;
  couponInput: HTMLInputElement;
  signatureInput: HTMLInputElement;
  status: HTMLElement;
  keyBox: HTMLElement;
  key: HTMLElement;
  orderPlan: HTMLElement;
  orderList: HTMLElement;
  orderDiscount: HTMLElement;
  orderAmount: HTMLElement;
  orderDestination: HTMLElement;
  orderReference: HTMLElement;
  orderExpires: HTMLElement;
  paidControls: HTMLElement;
  planButtons: HTMLButtonElement[];
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
  private selectedPlan = 'plus';
  private wallets: readonly Wallet[] = [];
  private selectedWallet: Wallet | null = null;
  private selectedAccount: WalletAccount | null = null;
  private order: PaymentOrder | null = null;

  constructor(nodes: PaymentNodes) {
    this.nodes = nodes;
    const activePlan = nodes.planButtons.find((button) => button.getAttribute('aria-pressed') === 'true');
    this.selectedPlan = activePlan?.dataset.solPlan || this.selectedPlan;
  }

  init() {
    this.renderWallets();
    this.walletRegistry.on('register', () => this.renderWallets());
    this.walletRegistry.on('unregister', () => this.renderWallets());
    this.nodes.form.addEventListener('submit', (event) => {
      event.preventDefault();
      void this.createOrder();
    });
    this.nodes.connectButton.addEventListener('click', () => void this.connectWallet());
    this.nodes.sendButton.addEventListener('click', () => void this.signAndSend());
    this.nodes.verifyButton.addEventListener('click', () => void this.verifySignature());
    this.nodes.revealButton.addEventListener('click', () => void this.revealActivationKey());
    this.nodes.copyButton.addEventListener('click', () => void this.copyActivationKey());
    this.nodes.signatureInput.addEventListener('input', () => {
      this.nodes.verifyButton.disabled = this.nodes.signatureInput.value.trim() === '' || !this.order || this.isZeroDueOrder(this.order);
    });
    this.nodes.walletSelect.addEventListener('change', () => {
      this.selectedWallet = this.wallets[Number(this.nodes.walletSelect.value)] || null;
      this.selectedAccount = null;
      this.updateSendState();
    });
    this.nodes.planButtons.forEach((button) => {
      button.addEventListener('click', () => this.selectPlan(button));
    });
  }

  private selectPlan(button: HTMLButtonElement) {
    this.selectedPlan = button.dataset.solPlan || this.selectedPlan;
    this.nodes.planButtons.forEach((item) => {
      const active = item === button;
      item.classList.toggle('active', active);
      item.setAttribute('aria-pressed', String(active));
    });
  }

  private renderWallets() {
    const chain = chainForCluster(this.order?.cluster || 'devnet');
    this.wallets = this.walletRegistry.get().filter((wallet) => walletSupportsSolana(wallet, chain));
    this.nodes.walletSelect.replaceChildren();

    if (this.wallets.length === 0) {
      this.nodes.walletSelect.append(new Option('No extension wallets found', ''));
      this.nodes.connectButton.disabled = true;
      this.selectedWallet = null;
      this.selectedAccount = null;
      this.updateSendState();
      return;
    }

    this.wallets.forEach((wallet, index) => {
      this.nodes.walletSelect.append(new Option(wallet.name, String(index)));
    });
    this.nodes.connectButton.disabled = false;
    this.selectedWallet = this.wallets[0] || null;
    this.updateSendState();
  }

  private async createOrder() {
    const guildID = this.nodes.guildInput.value.trim();
    if (!guildID) {
      this.setStatus('Discord server ID is required.', 'error');
      this.nodes.guildInput.focus();
      return;
    }

    this.setBusy(true);
    this.setStatus('Preparing server payment order.');
    try {
      const order = await requestJSON<PaymentOrder>(this.apiURL('/billing/sol/orders'), {
        method: 'POST',
        body: JSON.stringify({
          guild_id: guildID,
          billing_owner_user_id: this.nodes.ownerInput.value.trim(),
          plan: this.selectedPlan,
          support_email: this.nodes.emailInput.value.trim(),
          coupon_code: this.nodes.couponInput.value.trim(),
        }),
      });
      this.order = order;
      this.nodes.signatureInput.value = '';
      this.nodes.verifyButton.disabled = true;
      this.nodes.revealButton.disabled = true;
      this.nodes.keyBox.hidden = true;
      this.renderOrder(order);
      this.renderWallets();
      if (this.isZeroDueOrder(order)) {
        this.nodes.revealButton.disabled = false;
        this.setStatus('Discount covers this plan. Reveal the activation key when ready.');
      } else {
        this.setStatus('Payment order ready. Connect a wallet to sign the server-built transaction.');
      }
    } catch (error) {
      this.setStatus(readableError(error), 'error');
    } finally {
      this.setBusy(false);
    }
  }

  private async connectWallet() {
    const wallet = this.selectedWallet;
    if (!wallet) {
      this.setStatus('No compatible wallet extension was found.', 'error');
      return;
    }

    this.setBusy(true);
    this.setStatus(`Connecting ${wallet.name}.`);
    try {
      const connect = feature<ConnectFeature>(wallet, StandardConnect);
      if (!connect) throw new Error(`${wallet.name} does not expose wallet-standard connect.`);
      const output = await connect.connect();
      const chain = chainForCluster(this.order?.cluster || 'devnet');
      const account = output.accounts.find((candidate) => accountSupports(candidate, chain));
      if (!account) throw new Error(`${wallet.name} did not authorize a Solana account for ${chain}.`);
      this.selectedAccount = account;
      this.setStatus(`Connected ${wallet.name}: ${shortAddress(account.address)}.`);
      this.updateSendState();
    } catch (error) {
      this.selectedAccount = null;
      this.updateSendState();
      this.setStatus(readableError(error), 'error');
    } finally {
      this.setBusy(false);
    }
  }

  private async signAndSend() {
    if (!this.order || !this.selectedWallet || !this.selectedAccount) {
      this.setStatus('Create an order and connect a wallet first.', 'error');
      return;
    }
    if (this.isZeroDueOrder(this.order)) {
      this.setStatus('This order is fully discounted; reveal the activation key instead.');
      return;
    }

    this.setBusy(true);
    this.setStatus('Requesting server-built SOL transaction.');
    try {
      const prepared = await requestJSON<PaymentTransaction>(this.apiURL(`/billing/sol/orders/${this.order.order_id}/transaction`), {
        method: 'POST',
        body: JSON.stringify({ payer_wallet: this.selectedAccount.address }),
      });
      this.order = prepared.order;
      this.renderOrder(prepared.order);
      const chain = chainForCluster(this.order.cluster);
      const signedTransaction = await this.signWithWallet(this.selectedWallet, this.selectedAccount, base64ToBytes(prepared.transaction), chain);
      this.setStatus('Submitting signed transaction through Panda.');
      const result = await requestSubmission(
        this.apiURL(`/billing/sol/orders/${this.order.order_id}/submit`),
        bytesToBase64(signedTransaction),
      );
      this.order = result.order;
      this.renderOrder(result.order);
      if (result.submitted_signature) {
        this.nodes.signatureInput.value = result.submitted_signature;
      }
      if (result.verified) {
        this.nodes.revealButton.disabled = false;
        this.nodes.verifyButton.disabled = true;
        this.setStatus('Payment verified. Activation key can be revealed once.');
        return;
      }
      this.nodes.verifyButton.disabled = !this.nodes.signatureInput.value.trim();
      this.setStatus(verificationMessage(result), result.failure_code === 'pending_confirmation' ? 'neutral' : 'error');
    } catch (error) {
      this.setStatus(readableError(error), 'error');
    } finally {
      this.setBusy(false);
    }
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

    throw new Error(`${wallet.name} cannot sign server-submitted SOL transfers from this page.`);
  }

  private async verifySignature() {
    if (!this.order) {
      this.setStatus('Create a payment order first.', 'error');
      return;
    }
    const signature = this.nodes.signatureInput.value.trim();
    if (!signature) {
      this.setStatus('Transaction signature is required.', 'error');
      return;
    }

    this.setBusy(true);
    this.setStatus('Checking transaction against the order.');
    try {
      const result = await requestVerification(this.apiURL(`/billing/sol/orders/${this.order.order_id}/verify`), signature);
      this.order = result.order;
      this.renderOrder(result.order);
      if (result.verified) {
        this.nodes.revealButton.disabled = false;
        this.setStatus('Payment verified. Activation key can be revealed once.');
        return;
      }
      this.nodes.revealButton.disabled = true;
      this.setStatus(verificationMessage(result), result.failure_code === 'pending_confirmation' ? 'neutral' : 'error');
    } catch (error) {
      this.setStatus(readableError(error), 'error');
    } finally {
      this.setBusy(false);
    }
  }

  private async revealActivationKey() {
    if (!this.order) return;
    this.setBusy(true);
    this.setStatus('Revealing one-time activation key.');
    try {
      const reveal = await requestJSON<ActivationKeyReveal>(this.apiURL(`/billing/sol/orders/${this.order.order_id}/activation-key`), {
        method: 'POST',
      });
      this.order = reveal.order;
      this.renderOrder(reveal.order);
      this.nodes.key.textContent = reveal.api_key;
      this.nodes.keyBox.hidden = false;
      this.nodes.revealButton.disabled = true;
      this.setStatus('Activation key revealed. Use it with /billing action:activate in Discord.');
    } catch (error) {
      this.setStatus(readableError(error), 'error');
    } finally {
      this.setBusy(false);
    }
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

  private renderOrder(order: PaymentOrder) {
    this.nodes.orderPlan.textContent = order.display_name || order.plan;
    this.nodes.orderList.textContent = formatLamportsText(order.list_lamports);
    this.nodes.orderDiscount.textContent = order.discount_lamports > 0 ? `-${formatLamportsText(order.discount_lamports)}` : '0 SOL';
    this.nodes.orderAmount.textContent = `${order.amount_sol} SOL`;
    this.nodes.orderDestination.textContent = order.destination_wallet ? shortAddress(order.destination_wallet) : '--';
    this.nodes.orderDestination.title = order.destination_wallet;
    this.nodes.orderReference.textContent = order.reference ? shortAddress(order.reference) : '--';
    this.nodes.orderReference.title = order.reference;
    this.nodes.orderExpires.textContent = new Intl.DateTimeFormat(undefined, {
      month: 'short',
      day: 'numeric',
      hour: 'numeric',
      minute: '2-digit',
    }).format(new Date(order.expires_at));
    this.nodes.paidControls.hidden = this.isZeroDueOrder(order);
    this.updateSendState();
  }

  private updateSendState() {
    this.nodes.sendButton.disabled = !this.order || this.isZeroDueOrder(this.order) || !this.selectedWallet || !this.selectedAccount;
  }

  private setBusy(busy: boolean) {
    this.nodes.root.classList.toggle('busy', busy);
    this.nodes.form.querySelectorAll<HTMLInputElement | HTMLButtonElement>('input, button').forEach((node) => {
      node.disabled = busy;
    });
    this.nodes.connectButton.disabled = busy || this.wallets.length === 0;
    this.nodes.walletSelect.disabled = busy || this.wallets.length === 0;
    if (busy) {
      this.nodes.sendButton.disabled = true;
      this.nodes.verifyButton.disabled = true;
      this.nodes.revealButton.disabled = true;
      return;
    }
    this.updateSendState();
    this.nodes.verifyButton.disabled = this.nodes.signatureInput.value.trim() === '' || !this.order || this.isZeroDueOrder(this.order);
    this.nodes.revealButton.disabled = this.order?.status !== 'verified';
  }

  private setStatus(message: string, tone: 'neutral' | 'error' = 'neutral') {
    this.nodes.status.textContent = message;
    this.nodes.status.dataset.tone = tone;
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
  const walletSelect = root.querySelector<HTMLSelectElement>('[data-sol-wallet-select]');
  const connectButton = root.querySelector<HTMLButtonElement>('[data-sol-connect]');
  const sendButton = root.querySelector<HTMLButtonElement>('[data-sol-send]');
  const verifyButton = root.querySelector<HTMLButtonElement>('[data-sol-verify]');
  const revealButton = root.querySelector<HTMLButtonElement>('[data-sol-reveal]');
  const copyButton = root.querySelector<HTMLButtonElement>('[data-sol-copy]');
  const guildInput = root.querySelector<HTMLInputElement>('[data-sol-guild]');
  const ownerInput = root.querySelector<HTMLInputElement>('[data-sol-owner]');
  const emailInput = root.querySelector<HTMLInputElement>('[data-sol-email]');
  const couponInput = root.querySelector<HTMLInputElement>('[data-sol-coupon]');
  const signatureInput = root.querySelector<HTMLInputElement>('[data-sol-signature]');
  const status = root.querySelector<HTMLElement>('[data-sol-status]');
  const keyBox = root.querySelector<HTMLElement>('[data-sol-key-box]');
  const key = root.querySelector<HTMLElement>('[data-sol-key]');
  const orderPlan = root.querySelector<HTMLElement>('[data-sol-order-plan]');
  const orderList = root.querySelector<HTMLElement>('[data-sol-order-list]');
  const orderDiscount = root.querySelector<HTMLElement>('[data-sol-order-discount]');
  const orderAmount = root.querySelector<HTMLElement>('[data-sol-order-amount]');
  const orderDestination = root.querySelector<HTMLElement>('[data-sol-order-destination]');
  const orderReference = root.querySelector<HTMLElement>('[data-sol-order-reference]');
  const orderExpires = root.querySelector<HTMLElement>('[data-sol-order-expires]');
  const paidControls = root.querySelector<HTMLElement>('[data-sol-paid-controls]');
  const planButtons = Array.from(root.querySelectorAll<HTMLButtonElement>('[data-sol-plan]'));

  if (
    !form ||
    !walletSelect ||
    !connectButton ||
    !sendButton ||
    !verifyButton ||
    !revealButton ||
    !copyButton ||
    !guildInput ||
    !ownerInput ||
    !emailInput ||
    !couponInput ||
    !signatureInput ||
    !status ||
    !keyBox ||
    !key ||
    !orderPlan ||
    !orderList ||
    !orderDiscount ||
    !orderAmount ||
    !orderDestination ||
    !orderReference ||
    !orderExpires ||
    !paidControls ||
    planButtons.length === 0
  ) {
    return null;
  }

  return {
    root,
    form,
    walletSelect,
    connectButton,
    sendButton,
    verifyButton,
    revealButton,
    copyButton,
    guildInput,
    ownerInput,
    emailInput,
    couponInput,
    signatureInput,
    status,
    keyBox,
    key,
    orderPlan,
    orderList,
    orderDiscount,
    orderAmount,
    orderDestination,
    orderReference,
    orderExpires,
    paidControls,
    planButtons,
  };
};

const walletSupportsSolana = (wallet: Wallet, chain: IdentifierString): boolean => {
  const signTransaction = feature<SignTransactionFeature>(wallet, SolanaSignTransaction);
  return wallet.chains.includes(chain) &&
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
    return 'Transaction found, waiting for confirmation.';
  }
  if (result.failure_error) return result.failure_error;
  return result.failure_code || 'Payment could not be verified.';
};

const readableError = (error: unknown): string => {
  if (error instanceof Error && error.message) return error.message;
  return 'Something went wrong.';
};

const formatLamportsText = (lamports: number): string => {
  if (!Number.isFinite(lamports) || lamports <= 0) return '0 SOL';
  const whole = Math.trunc(lamports / 1_000_000_000);
  const fraction = Math.trunc(lamports % 1_000_000_000).toString().padStart(9, '0').replace(/0+$/, '');
  return `${fraction ? `${whole}.${fraction}` : String(whole)} SOL`;
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

const shortAddress = (address: string): string => {
  if (address.length <= 12) return address;
  return `${address.slice(0, 4)}...${address.slice(-4)}`;
};
