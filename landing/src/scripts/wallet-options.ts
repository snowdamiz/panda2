import type { Wallet } from '@wallet-standard/base';

export const createWalletOption = (wallet: Wallet): HTMLButtonElement => {
  const option = document.createElement('button');
  option.type = 'button';
  option.className = 'account-wallet-option';

  const icon = document.createElement('img');
  icon.className = 'account-wallet-option-icon';
  icon.src = wallet.icon;
  icon.alt = '';
  icon.width = 24;
  icon.height = 24;
  icon.decoding = 'async';

  const label = document.createElement('span');
  label.textContent = wallet.name;

  option.append(icon, label);
  return option;
};
