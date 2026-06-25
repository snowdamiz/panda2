import {
  formatDate,
  formatStatus,
  messageForError,
  readableError,
  type AdminPanel,
  type AdminPanelContext,
} from './admin-session';

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

type CouponNodes = {
  couponForm: HTMLFormElement;
  planButtons: HTMLButtonElement[];
  discountInput: HTMLInputElement;
  couponCodeInput: HTMLInputElement;
  maxRedemptionsInput: HTMLInputElement;
  expiresInput: HTMLInputElement;
  noteInput: HTMLInputElement;
  fillDiscountButton: HTMLButtonElement;
  count: HTMLElement;
  list: HTMLElement;
  created: HTMLElement;
  createdCode: HTMLElement;
  copyCodeButton: HTMLButtonElement;
};

// CouponsPanel renders the operator coupon workspace. Authentication and the
// shared status/busy state are owned by the admin console (see admin-console).
export class CouponsPanel implements AdminPanel {
  private readonly nodes: CouponNodes;
  private readonly ctx: AdminPanelContext;
  private planLamports: Record<string, number> = {};
  private selectedPlan = 'plus';

  static fromRoot(root: HTMLElement, ctx: AdminPanelContext): CouponsPanel | null {
    const nodes = collectCouponNodes(root);
    if (!nodes) return null;
    return new CouponsPanel(nodes, ctx);
  }

  private constructor(nodes: CouponNodes, ctx: AdminPanelContext) {
    this.nodes = nodes;
    this.ctx = ctx;
  }

  init() {
    this.selectedPlan = this.nodes.planButtons.find((button) => button.classList.contains('active'))?.dataset.adminPlan || this.selectedPlan;

    this.nodes.planButtons.forEach((button) => {
      button.addEventListener('click', () => this.selectPlan(button.dataset.adminPlan || 'plus'));
    });
    this.nodes.fillDiscountButton.addEventListener('click', () => this.fillPlanDiscount());
    this.nodes.copyCodeButton.addEventListener('click', () => void this.copyCreatedCode());
    this.nodes.couponForm.addEventListener('submit', (event) => {
      event.preventDefault();
      void this.createCoupon();
    });
  }

  reset() {
    this.nodes.count.textContent = '--';
    this.nodes.list.replaceChildren();
    this.nodes.created.hidden = true;
  }

  async load() {
    this.ctx.setBusy(true);
    this.ctx.setStatus('Loading coupons.');
    try {
      const response = await this.ctx.request<AdminCouponListResponse>('/admin/coupons');
      this.planLamports = response.plan_lamports || {};
      this.renderCoupons(response.coupons || []);
      this.fillPlanDiscount(false);
      this.ctx.setStatus('Coupons loaded.');
    } catch (error) {
      this.handleError(error);
    } finally {
      this.ctx.setBusy(false);
    }
  }

  private async createCoupon() {
    if (!this.nodes.couponForm.reportValidity()) return;
    this.ctx.setBusy(true);
    this.ctx.setStatus('Creating coupon.');
    try {
      const body = {
        plan: this.selectedPlan,
        discount_lamports: integerValue(this.nodes.discountInput.value),
        coupon_code: this.nodes.couponCodeInput.value.trim(),
        max_redemptions: integerValue(this.nodes.maxRedemptionsInput.value),
        expires_at: this.nodes.expiresInput.value.trim(),
        note: this.nodes.noteInput.value.trim(),
      };
      const response = await this.ctx.request<AdminCouponCreateResponse>('/admin/coupons', {
        method: 'POST',
        body: JSON.stringify(body),
      });
      this.renderCreatedCode(response.code);
      this.nodes.couponForm.reset();
      this.selectPlan(this.selectedPlan);
      await this.load();
      this.ctx.setStatus(`Coupon ${response.coupon.coupon_id} created.`);
    } catch (error) {
      this.handleError(error);
    } finally {
      this.ctx.setBusy(false);
    }
  }

  private async revokeCoupon(coupon: AdminCoupon) {
    if (coupon.status === 'revoked') return;
    const confirmed = window.confirm(`Revoke coupon ${coupon.coupon_id}?`);
    if (!confirmed) return;
    this.ctx.setBusy(true);
    this.ctx.setStatus('Revoking coupon.');
    try {
      await this.ctx.request<AdminCoupon>(`/admin/coupons/${encodeURIComponent(coupon.coupon_id)}/revoke`, {
        method: 'POST',
      });
      await this.load();
      this.ctx.setStatus(`Coupon ${coupon.coupon_id} revoked.`);
    } catch (error) {
      this.handleError(error);
    } finally {
      this.ctx.setBusy(false);
    }
  }

  private renderCoupons(coupons: AdminCoupon[]) {
    this.nodes.count.textContent = coupons.length === 1 ? '1 coupon' : `${coupons.length} coupons`;
    this.nodes.list.replaceChildren();
    if (coupons.length === 0) {
      this.nodes.list.append(emptyRow('No coupons yet.'));
      return;
    }
    coupons.forEach((coupon) => this.nodes.list.append(this.renderCoupon(coupon)));
  }

  private renderCoupon(coupon: AdminCoupon): HTMLTableRowElement {
    const row = document.createElement('tr');
    row.className = 'admin-coupon-row';
    row.dataset.status = coupon.status;

    const primary = document.createElement('td');
    primary.className = 'admin-cell-primary';
    const id = document.createElement('strong');
    id.textContent = coupon.coupon_id;
    const prefix = document.createElement('span');
    prefix.textContent = `${coupon.code_prefix}...`;
    primary.append(id, prefix);
    if (coupon.owner_note) {
      const note = document.createElement('small');
      note.textContent = coupon.owner_note;
      primary.append(note);
    }

    const planCell = cell(coupon.display_name || formatStatus(coupon.plan));
    const discountCell = cell(formatLamports(coupon.discount_lamports));
    discountCell.className = 'admin-cell-mono';
    const limitCell = cell(coupon.max_redemptions > 0 ? String(coupon.max_redemptions) : 'Unlimited');
    const usedCell = cell(`${coupon.consumed} used · ${coupon.pending} pending`);

    const expiresCell = cell(formatDate(coupon.expires_at));

    const statusCell = document.createElement('td');
    const status = document.createElement('span');
    status.className = 'admin-status-pill';
    status.dataset.status = coupon.status;
    status.textContent = formatStatus(coupon.status);
    statusCell.append(status);

    const actionCell = document.createElement('td');
    actionCell.className = 'admin-cell-action';
    const revoke = document.createElement('button');
    revoke.type = 'button';
    revoke.className = 'admin-row-action admin-row-action-danger';
    revoke.textContent = coupon.status === 'revoked' ? 'Revoked' : 'Revoke';
    revoke.disabled = coupon.status === 'revoked';
    revoke.addEventListener('click', () => void this.revokeCoupon(coupon));
    actionCell.append(revoke);

    row.append(primary, planCell, discountCell, limitCell, usedCell, expiresCell, statusCell, actionCell);
    return row;
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
      if (overwrite) this.ctx.setStatus('Plan lamports are not available from the API.', 'error');
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
      this.ctx.setStatus('Coupon code copied.');
    } catch {
      this.ctx.setStatus('Clipboard access was blocked; select the code manually.', 'error');
    }
  }

  private handleError(error: unknown) {
    this.ctx.setStatus(messageForError(readableError(error)), 'error');
  }
}

const collectCouponNodes = (root: HTMLElement): CouponNodes | null => {
  const couponForm = root.querySelector<HTMLFormElement>('[data-admin-coupon-form]');
  const planButtons = Array.from(root.querySelectorAll<HTMLButtonElement>('[data-admin-plan]'));
  const discountInput = root.querySelector<HTMLInputElement>('[data-admin-discount]');
  const couponCodeInput = root.querySelector<HTMLInputElement>('[data-admin-coupon-code]');
  const maxRedemptionsInput = root.querySelector<HTMLInputElement>('[data-admin-max-redemptions]');
  const expiresInput = root.querySelector<HTMLInputElement>('[data-admin-expires]');
  const noteInput = root.querySelector<HTMLInputElement>('[data-admin-note]');
  const fillDiscountButton = root.querySelector<HTMLButtonElement>('[data-admin-fill-discount]');
  const count = root.querySelector<HTMLElement>('[data-admin-count]');
  const list = root.querySelector<HTMLElement>('[data-admin-coupon-list]');
  const created = root.querySelector<HTMLElement>('[data-admin-created]');
  const createdCode = root.querySelector<HTMLElement>('[data-admin-created-code]');
  const copyCodeButton = root.querySelector<HTMLButtonElement>('[data-admin-copy-code]');
  if (
    !couponForm ||
    planButtons.length === 0 ||
    !discountInput ||
    !couponCodeInput ||
    !maxRedemptionsInput ||
    !expiresInput ||
    !noteInput ||
    !fillDiscountButton ||
    !count ||
    !list ||
    !created ||
    !createdCode ||
    !copyCodeButton
  ) {
    return null;
  }
  return {
    couponForm,
    planButtons,
    discountInput,
    couponCodeInput,
    maxRedemptionsInput,
    expiresInput,
    noteInput,
    fillDiscountButton,
    count,
    list,
    created,
    createdCode,
    copyCodeButton,
  };
};

const COUPON_COLUMNS = 8;

const cell = (value: string): HTMLTableCellElement => {
  const element = document.createElement('td');
  element.textContent = value;
  return element;
};

const emptyRow = (message: string): HTMLTableRowElement => {
  const row = document.createElement('tr');
  row.className = 'admin-empty-row';
  const element = document.createElement('td');
  element.className = 'admin-empty-cell';
  element.colSpan = COUPON_COLUMNS;
  element.textContent = message;
  row.append(element);
  return row;
};

const integerValue = (value: string): number => {
  const parsed = Number.parseInt(value.trim(), 10);
  return Number.isFinite(parsed) ? parsed : 0;
};

const formatLamports = (value: number): string => {
  if (!Number.isFinite(value)) return '--';
  return `${new Intl.NumberFormat().format(value)} lamports`;
};
