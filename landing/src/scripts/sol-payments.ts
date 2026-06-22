import { getWallets } from '@wallet-standard/app';
import type { IdentifierString, Wallet, WalletAccount } from '@wallet-standard/base';
import { StandardConnect } from '@wallet-standard/features';
import { SolanaSignAndSendTransaction, SolanaSignTransaction } from '@solana/wallet-standard-features';
import bs58 from 'bs58';
import { Buffer } from 'buffer';
import {
  Connection,
  PublicKey,
  SystemProgram,
  Transaction,
  TransactionInstruction,
} from '@solana/web3.js';

type PaymentOrder = {
  order_id: string;
  guild_id: string;
  billing_owner_user_id?: string;
  plan: string;
  display_name: string;
  expected_lamports: number;
  amount_sol: string;
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
  failure_code?: string;
  failure_error?: string;
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

type SignAndSendTransactionFeature = {
  readonly supportedTransactionVersions: readonly ('legacy' | 0)[];
  signAndSendTransaction(
    ...inputs: readonly {
      readonly account: WalletAccount;
      readonly transaction: Uint8Array;
      readonly chain: string;
      readonly options?: {
        readonly commitment?: 'confirmed' | 'finalized';
        readonly preflightCommitment?: 'confirmed' | 'finalized';
        readonly skipPreflight?: boolean;
        readonly maxRetries?: number;
      };
    }[]
  ): Promise<readonly { readonly signature: Uint8Array }[]>;
};

type PaymentNodes = {
  root: HTMLElement;
  form: HTMLFormElement;
  walletSelect: HTMLSelectElement;
  connectButton: HTMLButtonElement;
  sendButton: HTMLButtonElement;
  deeplink: HTMLAnchorElement;
  verifyButton: HTMLButtonElement;
  revealButton: HTMLButtonElement;
  copyButton: HTMLButtonElement;
  guildInput: HTMLInputElement;
  ownerInput: HTMLInputElement;
  emailInput: HTMLInputElement;
  signatureInput: HTMLInputElement;
  status: HTMLElement;
  keyBox: HTMLElement;
  key: HTMLElement;
  orderPlan: HTMLElement;
  orderAmount: HTMLElement;
  orderDestination: HTMLElement;
  orderReference: HTMLElement;
  orderExpires: HTMLElement;
  planButtons: HTMLButtonElement[];
};

const MEMO_PROGRAM_ID = new PublicKey('MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr');

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
      this.nodes.verifyButton.disabled = this.nodes.signatureInput.value.trim() === '' || !this.order;
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
        }),
      });
      this.order = order;
      this.nodes.signatureInput.value = '';
      this.nodes.verifyButton.disabled = true;
      this.nodes.revealButton.disabled = true;
      this.nodes.keyBox.hidden = true;
      this.renderOrder(order);
      this.renderWallets();
      this.setStatus('Payment order ready. Connect a wallet or open the wallet app link.');
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

    this.setBusy(true);
    this.setStatus('Building exact SOL transfer.');
    try {
      const transaction = await this.buildTransaction(this.order, this.selectedAccount);
      const serialized = transaction.serialize({ requireAllSignatures: false, verifySignatures: false });
      const chain = chainForCluster(this.order.cluster);
      const signature = await this.signWithBestWalletPath(this.selectedWallet, this.selectedAccount, serialized, chain, transaction);
      this.nodes.signatureInput.value = signature;
      this.nodes.verifyButton.disabled = false;
      this.setStatus('Transaction submitted. Verifying with Panda.');
      await this.verifySignature();
    } catch (error) {
      this.setStatus(readableError(error), 'error');
    } finally {
      this.setBusy(false);
    }
  }

  private async signWithBestWalletPath(
    wallet: Wallet,
    account: WalletAccount,
    serialized: Uint8Array,
    chain: string,
    transaction: Transaction,
  ): Promise<string> {
    const signTransaction = feature<SignTransactionFeature>(wallet, SolanaSignTransaction);
    if (signTransaction && supportsLegacy(signTransaction.supportedTransactionVersions) && account.features.includes(SolanaSignTransaction)) {
      const [output] = await signTransaction.signTransaction({
        account,
        transaction: serialized,
        chain,
        options: { preflightCommitment: 'confirmed' },
      });
      if (!output) throw new Error('Wallet did not return a signed transaction.');
      const connection = this.connectionForOrder();
      const signed = Transaction.from(output.signedTransaction);
      const simulation = await connection.simulateTransaction(signed);
      if (simulation.value.err) {
        throw new Error(`Simulation failed: ${JSON.stringify(simulation.value.err)}`);
      }
      const signature = await connection.sendRawTransaction(output.signedTransaction, {
        skipPreflight: false,
        preflightCommitment: 'confirmed',
      });
      await connection.confirmTransaction(
        {
          signature,
          blockhash: transaction.recentBlockhash || '',
          lastValidBlockHeight: this.lastValidBlockHeight(transaction),
        },
        'confirmed',
      );
      return signature;
    }

    const signAndSend = feature<SignAndSendTransactionFeature>(wallet, SolanaSignAndSendTransaction);
    if (signAndSend && supportsLegacy(signAndSend.supportedTransactionVersions) && account.features.includes(SolanaSignAndSendTransaction)) {
      const [output] = await signAndSend.signAndSendTransaction({
        account,
        transaction: serialized,
        chain,
        options: {
          commitment: 'confirmed',
          preflightCommitment: 'confirmed',
          skipPreflight: false,
          maxRetries: 3,
        },
      });
      if (!output) throw new Error('Wallet did not return a transaction signature.');
      return bs58.encode(output.signature);
    }

    throw new Error(`${wallet.name} cannot sign SOL transfers from this page. Use the wallet app link, then paste the signature.`);
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

  private async buildTransaction(order: PaymentOrder, account: WalletAccount): Promise<Transaction> {
    if (!Number.isSafeInteger(order.expected_lamports) || order.expected_lamports <= 0) {
      throw new Error('Order amount is not a safe lamport value.');
    }
    const payer = new PublicKey(account.address);
    const destination = new PublicKey(order.destination_wallet);
    const connection = this.connectionForOrder();
    const latestBlockhash = await connection.getLatestBlockhash('confirmed');
    const transaction = new Transaction({
      feePayer: payer,
      recentBlockhash: latestBlockhash.blockhash,
    });
    transaction.add(
      SystemProgram.transfer({
        fromPubkey: payer,
        toPubkey: destination,
        lamports: order.expected_lamports,
      }),
      new TransactionInstruction({
        keys: [],
        programId: MEMO_PROGRAM_ID,
        data: Buffer.from(order.memo || order.reference, 'utf8'),
      }),
    );
    transaction.lastValidBlockHeight = latestBlockhash.lastValidBlockHeight;
    return transaction;
  }

  private connectionForOrder(): Connection {
    const rpcURL = this.nodes.root.dataset.rpcUrl?.trim() || rpcURLForCluster(this.order?.cluster || 'devnet');
    return new Connection(rpcURL, 'confirmed');
  }

  private lastValidBlockHeight(transaction: Transaction): number {
    const value = transaction.lastValidBlockHeight;
    if (typeof value !== 'number') throw new Error('Transaction block height was not set.');
    return value;
  }

  private renderOrder(order: PaymentOrder) {
    this.nodes.orderPlan.textContent = order.display_name || order.plan;
    this.nodes.orderAmount.textContent = `${order.amount_sol} SOL`;
    this.nodes.orderDestination.textContent = shortAddress(order.destination_wallet);
    this.nodes.orderDestination.title = order.destination_wallet;
    this.nodes.orderReference.textContent = shortAddress(order.reference);
    this.nodes.orderReference.title = order.reference;
    this.nodes.orderExpires.textContent = new Intl.DateTimeFormat(undefined, {
      month: 'short',
      day: 'numeric',
      hour: 'numeric',
      minute: '2-digit',
    }).format(new Date(order.expires_at));
    this.nodes.deeplink.href = order.payment_url || '#';
    this.nodes.deeplink.hidden = !order.payment_url;
    this.updateSendState();
  }

  private updateSendState() {
    this.nodes.sendButton.disabled = !this.order || !this.selectedWallet || !this.selectedAccount;
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
    this.nodes.verifyButton.disabled = this.nodes.signatureInput.value.trim() === '' || !this.order;
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
}

const collectNodes = (root: HTMLElement): PaymentNodes | null => {
  const form = root.querySelector<HTMLFormElement>('[data-sol-form]');
  const walletSelect = root.querySelector<HTMLSelectElement>('[data-sol-wallet-select]');
  const connectButton = root.querySelector<HTMLButtonElement>('[data-sol-connect]');
  const sendButton = root.querySelector<HTMLButtonElement>('[data-sol-send]');
  const deeplink = root.querySelector<HTMLAnchorElement>('[data-sol-deeplink]');
  const verifyButton = root.querySelector<HTMLButtonElement>('[data-sol-verify]');
  const revealButton = root.querySelector<HTMLButtonElement>('[data-sol-reveal]');
  const copyButton = root.querySelector<HTMLButtonElement>('[data-sol-copy]');
  const guildInput = root.querySelector<HTMLInputElement>('[data-sol-guild]');
  const ownerInput = root.querySelector<HTMLInputElement>('[data-sol-owner]');
  const emailInput = root.querySelector<HTMLInputElement>('[data-sol-email]');
  const signatureInput = root.querySelector<HTMLInputElement>('[data-sol-signature]');
  const status = root.querySelector<HTMLElement>('[data-sol-status]');
  const keyBox = root.querySelector<HTMLElement>('[data-sol-key-box]');
  const key = root.querySelector<HTMLElement>('[data-sol-key]');
  const orderPlan = root.querySelector<HTMLElement>('[data-sol-order-plan]');
  const orderAmount = root.querySelector<HTMLElement>('[data-sol-order-amount]');
  const orderDestination = root.querySelector<HTMLElement>('[data-sol-order-destination]');
  const orderReference = root.querySelector<HTMLElement>('[data-sol-order-reference]');
  const orderExpires = root.querySelector<HTMLElement>('[data-sol-order-expires]');
  const planButtons = Array.from(root.querySelectorAll<HTMLButtonElement>('[data-sol-plan]'));

  if (
    !form ||
    !walletSelect ||
    !connectButton ||
    !sendButton ||
    !deeplink ||
    !verifyButton ||
    !revealButton ||
    !copyButton ||
    !guildInput ||
    !ownerInput ||
    !emailInput ||
    !signatureInput ||
    !status ||
    !keyBox ||
    !key ||
    !orderPlan ||
    !orderAmount ||
    !orderDestination ||
    !orderReference ||
    !orderExpires ||
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
    deeplink,
    verifyButton,
    revealButton,
    copyButton,
    guildInput,
    ownerInput,
    emailInput,
    signatureInput,
    status,
    keyBox,
    key,
    orderPlan,
    orderAmount,
    orderDestination,
    orderReference,
    orderExpires,
    planButtons,
  };
};

const walletSupportsSolana = (wallet: Wallet, chain: IdentifierString): boolean => {
  return wallet.chains.includes(chain) && Boolean(feature<ConnectFeature>(wallet, StandardConnect)) && (
    Boolean(feature<SignTransactionFeature>(wallet, SolanaSignTransaction)) ||
    Boolean(feature<SignAndSendTransactionFeature>(wallet, SolanaSignAndSendTransaction))
  );
};

const accountSupports = (account: WalletAccount, chain: IdentifierString): boolean => {
  return account.chains.includes(chain) && (
    account.features.includes(SolanaSignTransaction) ||
    account.features.includes(SolanaSignAndSendTransaction)
  );
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

const rpcURLForCluster = (cluster: string): string => {
  const normalized = cluster.trim().toLowerCase();
  if (normalized === 'mainnet' || normalized === 'mainnet-beta') return 'https://api.mainnet-beta.solana.com';
  if (normalized === 'testnet') return 'https://api.testnet.solana.com';
  return 'https://api.devnet.solana.com';
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

const shortAddress = (address: string): string => {
  if (address.length <= 12) return address;
  return `${address.slice(0, 4)}...${address.slice(-4)}`;
};
