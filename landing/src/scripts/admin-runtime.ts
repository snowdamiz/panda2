import {
  formatDate,
  messageForError,
  readableError,
  type AdminPanel,
  type AdminPanelContext,
} from './admin-session';

type AdminRuntimeStatus = {
  disabled: boolean;
  message: string;
  default_message: string;
  effective_message: string;
  updated_by: string;
  updated_at: string;
};

type RuntimeNodes = {
  form: HTMLFormElement;
  disabledInput: HTMLInputElement;
  messageInput: HTMLTextAreaElement;
  state: HTMLElement;
  card: HTMLElement;
  title: HTMLElement;
  effectiveMessage: HTMLElement;
  updated: HTMLElement;
  actor: HTMLElement;
  enableButton: HTMLButtonElement;
};

export class RuntimePanel implements AdminPanel {
  private readonly nodes: RuntimeNodes;
  private readonly ctx: AdminPanelContext;
  private defaultMessage = 'Panda is sleeping, maintenance in progress.';

  static fromRoot(root: HTMLElement, ctx: AdminPanelContext): RuntimePanel | null {
    const nodes = collectRuntimeNodes(root);
    if (!nodes) return null;
    return new RuntimePanel(nodes, ctx);
  }

  private constructor(nodes: RuntimeNodes, ctx: AdminPanelContext) {
    this.nodes = nodes;
    this.ctx = ctx;
  }

  init() {
    this.nodes.form.addEventListener('submit', (event) => {
      event.preventDefault();
      void this.save(this.nodes.disabledInput.checked);
    });
    this.nodes.enableButton.addEventListener('click', () => {
      this.nodes.disabledInput.checked = false;
      void this.save(false);
    });
  }

  reset() {
    this.nodes.state.textContent = '--';
    this.nodes.title.textContent = '--';
    this.nodes.effectiveMessage.textContent = '';
    this.nodes.updated.textContent = '--';
    this.nodes.actor.textContent = '--';
    this.nodes.disabledInput.checked = false;
    this.nodes.messageInput.value = '';
    this.nodes.card.dataset.state = '';
    this.syncEnableButton();
  }

  async load() {
    this.ctx.setBusy(true);
    this.ctx.setStatus('Loading runtime state.');
    try {
      const status = await this.ctx.request<AdminRuntimeStatus>('/admin/runtime');
      this.render(status);
      this.ctx.setStatus('Runtime state loaded.');
    } catch (error) {
      this.handleError(error);
    } finally {
      this.ctx.setBusy(false);
      this.syncEnableButton();
    }
  }

  private async save(disabled: boolean) {
    if (!this.nodes.form.reportValidity()) return;
    this.ctx.setBusy(true);
    this.ctx.setStatus(disabled ? 'Disabling Panda.' : 'Enabling Panda.');
    try {
      const status = await this.ctx.request<AdminRuntimeStatus>('/admin/runtime', {
        method: 'POST',
        body: JSON.stringify({
          disabled,
          message: this.nodes.messageInput.value.trim(),
        }),
      });
      this.render(status);
      this.ctx.setStatus(disabled ? 'Panda maintenance mode enabled.' : 'Panda enabled.');
    } catch (error) {
      this.handleError(error);
    } finally {
      this.ctx.setBusy(false);
      this.syncEnableButton();
    }
  }

  private render(status: AdminRuntimeStatus) {
    this.defaultMessage = status.default_message || this.defaultMessage;
    this.nodes.disabledInput.checked = Boolean(status.disabled);
    this.nodes.messageInput.value = status.message || '';
    this.nodes.messageInput.placeholder = this.defaultMessage;
    this.nodes.card.dataset.state = status.disabled ? 'disabled' : 'enabled';
    this.nodes.state.textContent = status.disabled ? 'Maintenance' : 'Enabled';
    this.nodes.title.textContent = status.disabled ? 'Maintenance mode' : 'Panda enabled';
    this.nodes.effectiveMessage.textContent = status.effective_message || this.defaultMessage;
    this.nodes.updated.textContent = formatDate(status.updated_at);
    this.nodes.actor.textContent = status.updated_by || '--';
    this.syncEnableButton();
  }

  private syncEnableButton() {
    this.nodes.enableButton.disabled = !this.nodes.disabledInput.checked;
  }

  private handleError(error: unknown) {
    this.ctx.setStatus(messageForError(readableError(error)), 'error');
  }
}

const collectRuntimeNodes = (root: HTMLElement): RuntimeNodes | null => {
  const form = root.querySelector<HTMLFormElement>('[data-admin-runtime-form]');
  const disabledInput = root.querySelector<HTMLInputElement>('[data-admin-runtime-disabled]');
  const messageInput = root.querySelector<HTMLTextAreaElement>('[data-admin-runtime-message]');
  const state = root.querySelector<HTMLElement>('[data-admin-runtime-state]');
  const card = root.querySelector<HTMLElement>('[data-admin-runtime-card]');
  const title = root.querySelector<HTMLElement>('[data-admin-runtime-title]');
  const effectiveMessage = root.querySelector<HTMLElement>('[data-admin-runtime-effective-message]');
  const updated = root.querySelector<HTMLElement>('[data-admin-runtime-updated]');
  const actor = root.querySelector<HTMLElement>('[data-admin-runtime-actor]');
  const enableButton = root.querySelector<HTMLButtonElement>('[data-admin-runtime-enable]');
  if (!form || !disabledInput || !messageInput || !state || !card || !title || !effectiveMessage || !updated || !actor || !enableButton) {
    return null;
  }
  return {
    form,
    disabledInput,
    messageInput,
    state,
    card,
    title,
    effectiveMessage,
    updated,
    actor,
    enableButton,
  };
};
