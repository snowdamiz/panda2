import {
  formatDate,
  formatStatus,
  messageForError,
  readableError,
  type AdminPanel,
  type AdminPanelContext,
} from './admin-session';

type AdminGuildLimits = {
  credits: number;
  knowledge_storage_bytes: number;
  retention_days: number;
};

type AdminGuildUsage = {
  available_credits: number;
  reserved_credits: number;
  credits: number;
  knowledge_storage_bytes: number;
};

type AdminGuildBilling = {
  has_credit_account: boolean;
  pack: string;
  pack_display_name: string;
  status: string;
  stored_status: string;
  grace_state: string;
  payment_provider: string;
  period_start?: string;
  period_end?: string;
  trial_ends_at?: string;
  cancel_at_period_end: boolean;
  can_use_paid_features: boolean;
  read_only: boolean;
  billing_owner_user_id: string;
  email: string;
  limits?: AdminGuildLimits;
  usage: AdminGuildUsage;
};

type AdminGuild = {
  guild_id: string;
  name: string;
  install_status: string;
  owner_user_id: string;
  installed_by_user_id: string;
  locale: string;
  joined_at: string;
  left_at?: string;
  billing: AdminGuildBilling | null;
};

type AdminPack = {
  pack: string;
  display_name: string;
  price_cents: number;
  credits: number;
};

type AdminGuildListResponse = {
  guilds: AdminGuild[];
  total: number;
  limit: number;
  offset: number;
  pack_catalog: AdminPack[];
  statuses: string[];
};

type GuildNodes = {
  searchForm: HTMLFormElement;
  searchInput: HTMLInputElement;
  count: HTMLElement;
  list: HTMLElement;
};

// GuildsPanel renders every guild using Panda and lets an operator override a
// guild's credit pack, status, trial, and cancel flag.
export class GuildsPanel implements AdminPanel {
  private readonly nodes: GuildNodes;
  private readonly ctx: AdminPanelContext;
  private packCatalog: AdminPack[] = [];
  private statuses: string[] = [];
  private search = '';

  static fromRoot(root: HTMLElement, ctx: AdminPanelContext): GuildsPanel | null {
    const nodes = collectGuildNodes(root);
    if (!nodes) return null;
    return new GuildsPanel(nodes, ctx);
  }

  private constructor(nodes: GuildNodes, ctx: AdminPanelContext) {
    this.nodes = nodes;
    this.ctx = ctx;
  }

  init() {
    this.nodes.searchForm.addEventListener('submit', (event) => {
      event.preventDefault();
      this.search = this.nodes.searchInput.value.trim();
      void this.load();
    });
  }

  reset() {
    this.nodes.count.textContent = '--';
    this.nodes.list.replaceChildren();
  }

  async load() {
    this.ctx.setBusy(true);
    this.ctx.setStatus('Loading guilds.');
    try {
      const params = new URLSearchParams();
      if (this.search) params.set('q', this.search);
      const query = params.toString();
      const response = await this.ctx.request<AdminGuildListResponse>(`/admin/guilds${query ? `?${query}` : ''}`);
      this.packCatalog = response.pack_catalog || [];
      this.statuses = response.statuses || [];
      this.renderGuilds(response.guilds || [], response.total ?? 0);
      this.ctx.setStatus('Guilds loaded.');
    } catch (error) {
      this.handleError(error);
    } finally {
      this.ctx.setBusy(false);
    }
  }

  private renderGuilds(guilds: AdminGuild[], total: number) {
    const shown = guilds.length;
    this.nodes.count.textContent = total === shown
      ? `${total} ${total === 1 ? 'guild' : 'guilds'}`
      : `${shown} of ${total} guilds`;

    this.nodes.list.replaceChildren();
    if (guilds.length === 0) {
      this.nodes.list.append(emptyRow(this.search ? 'No guilds matched that search.' : 'No guilds yet.'));
      return;
    }
    guilds.forEach((guild) => this.nodes.list.append(this.renderGuild(guild)));
  }

  // renderGuild builds the table row plus a collapsed detail row that holds the
  // credit meters and the inline credit-account editor, returned together so the
  // tbody keeps them adjacent.
  private renderGuild(guild: AdminGuild): DocumentFragment {
    const billing = guild.billing;
    const fragment = document.createDocumentFragment();

    const row = document.createElement('tr');
    row.className = 'admin-guild-row';
    row.dataset.guildId = guild.guild_id;
    if (billing?.read_only) row.dataset.state = 'read_only';
    else if (billing?.can_use_paid_features) row.dataset.state = 'active';

    const primary = document.createElement('td');
    primary.className = 'admin-cell-primary';
    const name = document.createElement('strong');
    name.textContent = guild.name || 'Unnamed guild';
    const id = document.createElement('span');
    id.textContent = guild.guild_id;
    primary.append(name, id);

    const hasCreditAccount = Boolean(billing?.has_credit_account);
    const packCell = document.createElement('td');
    if (hasCreditAccount) {
      packCell.append(badge(billing?.pack_display_name || formatStatus(billing?.pack || ''), 'pack'));
    } else {
      packCell.append(badge('No credit account', 'pack'));
    }

    const statusCell = document.createElement('td');
    statusCell.append(badge(hasCreditAccount ? formatStatus(billing?.status || '') : 'None', 'status'));
    if (guild.install_status && guild.install_status !== 'active') {
      statusCell.append(badge(formatStatus(guild.install_status), 'install'));
    }

    const ownerCell = document.createElement('td');
    ownerCell.className = 'admin-cell-mono';
    ownerCell.textContent = guild.owner_user_id || '--';

    const expiresCell = document.createElement('td');
    expiresCell.textContent = formatDate(billing?.period_end);

    const actionCell = document.createElement('td');
    actionCell.className = 'admin-cell-action';
    const toggle = document.createElement('button');
    toggle.type = 'button';
    toggle.className = 'admin-row-toggle';
    toggle.textContent = 'Manage';
    toggle.setAttribute('aria-expanded', 'false');
    actionCell.append(toggle);

    row.append(primary, packCell, statusCell, ownerCell, expiresCell, actionCell);

    const detailRow = document.createElement('tr');
    detailRow.className = 'admin-detail-row';
    detailRow.hidden = true;
    const detailCell = document.createElement('td');
    detailCell.colSpan = GUILD_COLUMNS;
    detailCell.append(this.renderGuildDetail(guild, row, detailRow));
    detailRow.append(detailCell);

    toggle.addEventListener('click', () => {
      const willOpen = Boolean(detailRow.hidden);
      detailRow.hidden = !willOpen;
      row.classList.toggle('expanded', willOpen);
      toggle.setAttribute('aria-expanded', String(willOpen));
      toggle.textContent = willOpen ? 'Close' : 'Manage';
    });

    fragment.append(row, detailRow);
    return fragment;
  }

  private renderGuildDetail(guild: AdminGuild, row: HTMLElement, detailRow: HTMLElement): HTMLElement {
    const billing = guild.billing;
    const hasCreditAccount = Boolean(billing?.has_credit_account);
    const wrapper = document.createElement('div');
    wrapper.className = 'admin-detail';

    const accountCard = sectionCard('Account');
    const kv = document.createElement('dl');
    kv.className = 'admin-kv';
    kv.append(
      kvRow('Owner', guild.owner_user_id || '--'),
      kvRow('Installed by', guild.installed_by_user_id || '--'),
      kvRow('Billing owner', billing?.billing_owner_user_id || '--'),
      kvRow('Email', billing?.email || '--'),
      kvRow('Joined', formatDate(guild.joined_at)),
      kvRow('Expires', formatDate(billing?.period_end)),
    );
    if (hasCreditAccount) {
      kv.append(
        kvRow('Trial ends', formatDate(billing?.trial_ends_at)),
        kvRow('Cancel at period end', billing?.cancel_at_period_end ? 'Yes' : 'No'),
      );
    }
    accountCard.body.append(kv);
    wrapper.append(accountCard.card);

    if (hasCreditAccount && billing?.limits) {
      const usageCard = sectionCard('Credits');
      usageCard.body.append(this.renderUsage(billing));
      wrapper.append(usageCard.card);
    } else {
      accountCard.card.classList.add('admin-detail-card-wide');
    }

    const manageCard = sectionCard(hasCreditAccount ? 'Manage credit account' : 'Create credit account');
    manageCard.card.classList.add('admin-detail-card-wide');
    manageCard.body.append(this.renderManageForm(guild, row, detailRow));
    wrapper.append(manageCard.card);

    return wrapper;
  }

  private renderUsage(billing: AdminGuildBilling): HTMLElement {
    const usage = document.createElement('div');
    usage.className = 'admin-guild-usage';
    const limits = billing.limits!;
    const usedCredits = Math.max(0, (billing.usage.credits || limits.credits || 0) - (billing.usage.available_credits || 0));
    const knowledgeStorageBytes = Math.max(0, billing.usage.knowledge_storage_bytes || 0);
    usage.append(
      usageMeter('Credits used', usedCredits, billing.usage.credits || limits.credits),
      usageMeter('Reserved credits', billing.usage.reserved_credits || 0, billing.usage.credits || limits.credits),
      usageMeter('Knowledge storage', knowledgeStorageBytes, limits.knowledge_storage_bytes, formatBytes),
    );
    return usage;
  }

  private renderManageForm(guild: AdminGuild, row: HTMLElement, detailRow: HTMLElement): HTMLElement {
    const billing = guild.billing;
    const form = document.createElement('form');
    form.className = 'admin-guild-form';

    const packSelect = labeledSelect('Pack', this.packCatalog.map((pack) => ({
      value: pack.pack,
      label: pack.display_name || formatStatus(pack.pack),
    })), billing?.pack);

    const statusSelect = labeledSelect('Status', this.statuses.map((status) => ({
      value: status,
      label: formatStatus(status),
    })), billing?.stored_status);

    const periodEnd = labeledInput('Expires on', 'date', toDateValue(billing?.period_end));
    const trialEnd = labeledInput('Trial ends', 'date', toDateValue(billing?.trial_ends_at));

    const cancelWrapper = document.createElement('label');
    cancelWrapper.className = 'admin-guild-checkbox';
    const cancel = document.createElement('input');
    cancel.type = 'checkbox';
    cancel.checked = Boolean(billing?.cancel_at_period_end);
    const cancelText = document.createElement('span');
    cancelText.textContent = 'Cancel at period end';
    cancelWrapper.append(cancel, cancelText);

    const grid = document.createElement('div');
    grid.className = 'admin-guild-form-grid';
    grid.append(packSelect.field, statusSelect.field, periodEnd.field, trialEnd.field);

    const actions = document.createElement('div');
    actions.className = 'admin-guild-form-actions';
    const save = document.createElement('button');
    save.type = 'submit';
    save.className = 'app-primary';
    save.textContent = billing?.has_credit_account ? 'Save changes' : 'Create credit account';
    actions.append(cancelWrapper, save);

    form.append(grid, actions);
    form.addEventListener('submit', (event) => {
      event.preventDefault();
      void this.saveCreditAccount(guild, row, detailRow, {
        pack: packSelect.select.value,
        status: statusSelect.select.value,
        period_end: periodEnd.input.value,
        trial_ends_at: trialEnd.input.value,
        cancel_at_period_end: cancel.checked,
      });
    });

    return form;
  }

  private async saveCreditAccount(
    guild: AdminGuild,
    row: HTMLElement,
    detailRow: HTMLElement,
    values: { pack: string; status: string; period_end: string; trial_ends_at: string; cancel_at_period_end: boolean },
  ) {
    this.ctx.setBusy(true);
    this.ctx.setStatus(`Updating ${guild.name || guild.guild_id}.`);
    try {
      const updated = await this.ctx.request<AdminGuild>(`/admin/guilds/${encodeURIComponent(guild.guild_id)}/credit-account`, {
        method: 'POST',
        body: JSON.stringify({
          pack: values.pack,
          status: values.status,
          period_end: values.period_end,
          trial_ends_at: values.trial_ends_at,
          cancel_at_period_end: values.cancel_at_period_end,
        }),
      });
      // Re-render the guild and drop it back in place with its editor still open.
      const fragment = this.renderGuild(updated);
      const newRow = fragment.querySelector<HTMLElement>('.admin-guild-row');
      const newDetail = fragment.querySelector<HTMLElement>('.admin-detail-row');
      const newToggle = newRow?.querySelector<HTMLButtonElement>('.admin-row-toggle');
      if (newRow && newDetail && newToggle) {
        newDetail.hidden = false;
        newRow.classList.add('expanded');
        newToggle.setAttribute('aria-expanded', 'true');
        newToggle.textContent = 'Close';
      }
      detailRow.replaceWith(fragment);
      row.remove();
      this.ctx.setStatus(`Updated ${updated.name || updated.guild_id}.`);
    } catch (error) {
      this.handleError(error);
    } finally {
      this.ctx.setBusy(false);
    }
  }

  private handleError(error: unknown) {
    this.ctx.setStatus(messageForError(readableError(error)), 'error');
  }
}

const GUILD_COLUMNS = 6;

const emptyRow = (message: string): HTMLTableRowElement => {
  const row = document.createElement('tr');
  row.className = 'admin-empty-row';
  const cell = document.createElement('td');
  cell.className = 'admin-empty-cell';
  cell.colSpan = GUILD_COLUMNS;
  cell.textContent = message;
  row.append(cell);
  return row;
};

const collectGuildNodes = (root: HTMLElement): GuildNodes | null => {
  const searchForm = root.querySelector<HTMLFormElement>('[data-admin-guild-search-form]');
  const searchInput = root.querySelector<HTMLInputElement>('[data-admin-guild-search]');
  const count = root.querySelector<HTMLElement>('[data-admin-guild-count]');
  const list = root.querySelector<HTMLElement>('[data-admin-guild-list]');
  if (!searchForm || !searchInput || !count || !list) return null;
  return { searchForm, searchInput, count, list };
};

const sectionCard = (title: string): { card: HTMLElement; body: HTMLElement } => {
  const card = document.createElement('section');
  card.className = 'admin-detail-card';
  const heading = document.createElement('h3');
  heading.className = 'admin-detail-card-title';
  heading.textContent = title;
  const body = document.createElement('div');
  body.className = 'admin-detail-card-body';
  card.append(heading, body);
  return { card, body };
};

const kvRow = (label: string, value: string): HTMLDivElement => {
  const wrapper = document.createElement('div');
  wrapper.className = 'admin-kv-row';
  const term = document.createElement('dt');
  const description = document.createElement('dd');
  term.textContent = label;
  description.textContent = value;
  wrapper.append(term, description);
  return wrapper;
};

const badge = (text: string, kind: string): HTMLSpanElement => {
  const span = document.createElement('span');
  span.className = 'admin-guild-badge';
  span.dataset.kind = kind;
  span.textContent = text;
  return span;
};

const usageMeter = (
  label: string,
  used: number,
  limit: number,
  format: (value: number) => string = (value) => new Intl.NumberFormat().format(value),
): HTMLElement => {
  const wrapper = document.createElement('div');
  wrapper.className = 'admin-guild-meter';
  const head = document.createElement('div');
  head.className = 'admin-guild-meter-head';
  const name = document.createElement('span');
  name.textContent = label;
  const value = document.createElement('strong');
  value.textContent = `${format(used)} / ${format(limit)}`;
  head.append(name, value);
  const track = document.createElement('div');
  track.className = 'admin-guild-meter-track';
  const fill = document.createElement('div');
  fill.className = 'admin-guild-meter-fill';
  const ratio = limit > 0 ? Math.min(1, Math.max(0, used / limit)) : 0;
  fill.style.width = `${(ratio * 100).toFixed(1)}%`;
  if (ratio >= 0.9) fill.dataset.level = 'high';
  track.append(fill);
  wrapper.append(head, track);
  return wrapper;
};

type LabeledSelect = { field: HTMLLabelElement; select: HTMLSelectElement };

const labeledSelect = (
  label: string,
  options: { value: string; label: string }[],
  selected?: string,
): LabeledSelect => {
  const field = document.createElement('label');
  const span = document.createElement('span');
  span.textContent = label;
  const select = document.createElement('select');
  options.forEach((option) => {
    const element = document.createElement('option');
    element.value = option.value;
    element.textContent = option.label;
    if (selected && option.value === selected) element.selected = true;
    select.append(element);
  });
  field.append(span, select);
  return { field, select };
};

type LabeledInput = { field: HTMLLabelElement; input: HTMLInputElement };

const labeledInput = (label: string, type: string, value: string): LabeledInput => {
  const field = document.createElement('label');
  const span = document.createElement('span');
  span.textContent = label;
  const input = document.createElement('input');
  input.type = type;
  input.value = value;
  field.append(span, input);
  return { field, input };
};

const toDateValue = (value?: string | null): string => {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return date.toISOString().slice(0, 10);
};

const formatBytes = (value: number): string => {
  if (!Number.isFinite(value)) return '--';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  const rounded = unit === 0 ? size : Math.round(size * 10) / 10;
  return `${rounded} ${units[unit]}`;
};
