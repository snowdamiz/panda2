import { commandViews } from '../data/landing';

(() => {
  'use strict';

  const header = document.querySelector<HTMLElement>('[data-header]');
  const menuButton = document.querySelector<HTMLButtonElement>('[data-menu-button]');
  const mobileNav = document.querySelector<HTMLElement>('[data-mobile-nav]');
  const commandDetail = document.querySelector<HTMLElement>('[data-command-detail]');
  const commandViewMap = new Map<string, (typeof commandViews)[number]>(
    commandViews.map((view) => [view.key, view]),
  );

  const setHeaderState = () => {
    header?.classList.toggle('scrolled', window.scrollY > 20);
  };

  setHeaderState();
  window.addEventListener('scroll', setHeaderState, { passive: true });

  const closeMobileMenu = () => {
    if (!menuButton || !mobileNav) return;
    menuButton.setAttribute('aria-expanded', 'false');
    menuButton.setAttribute('aria-label', 'Open menu');
    mobileNav.hidden = true;
  };

  if (menuButton && mobileNav) {
    menuButton.addEventListener('click', () => {
      const opening = menuButton.getAttribute('aria-expanded') !== 'true';
      menuButton.setAttribute('aria-expanded', String(opening));
      menuButton.setAttribute('aria-label', opening ? 'Close menu' : 'Open menu');
      mobileNav.hidden = !opening;
    });

    mobileNav.querySelectorAll('a').forEach((link) => {
      link.addEventListener('click', closeMobileMenu);
    });
  }

  document.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape') return;
    closeMobileMenu();
  });

  const renderCommandDetail = (key: string) => {
    const view = commandViewMap.get(key);
    if (!commandDetail || !view) return;

    commandDetail.classList.add('changing');
    window.setTimeout(() => {
      commandDetail.querySelectorAll<HTMLElement>('.detail-value').forEach((value, index) => {
        const small = value.querySelector('small');
        const strong = value.querySelector('b');
        const content = view.values[index];
        if (!small || !strong || !content) return;
        small.textContent = content[0];
        strong.textContent = content[1];
      });

      const status = commandDetail.querySelector<HTMLElement>('.detail-status');
      if (status) {
        const dot = status.querySelector('i');
        status.textContent = view.status;
        if (dot) status.prepend(dot);
      }
      commandDetail.classList.remove('changing');
    }, 150);
  };

  document.querySelectorAll<HTMLButtonElement>('[data-command-row]').forEach((row) => {
    row.addEventListener('click', () => {
      document.querySelectorAll<HTMLButtonElement>('[data-command-row]').forEach((item) => {
        item.classList.remove('active');
        item.setAttribute('aria-pressed', 'false');
      });
      row.classList.add('active');
      row.setAttribute('aria-pressed', 'true');
      renderCommandDetail(row.dataset.command || 'behavior');
    });
  });

  const revealItems = document.querySelectorAll('.reveal');
  if ('IntersectionObserver' in window) {
    const revealObserver = new IntersectionObserver(
      (entries, observer) => {
        entries.forEach((entry) => {
          if (!entry.isIntersecting) return;
          entry.target.classList.add('revealed');
          observer.unobserve(entry.target);
        });
      },
      { threshold: 0.12, rootMargin: '0px 0px -30px' },
    );

    revealItems.forEach((item) => revealObserver.observe(item));
  } else {
    revealItems.forEach((item) => item.classList.add('revealed'));
  }

  document.querySelectorAll('[data-year]').forEach((node) => {
    node.textContent = String(new Date().getFullYear());
  });
})();
