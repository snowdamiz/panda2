import { commandViews, modelOptions } from '../data/landing';

(() => {
  'use strict';

  const header = document.querySelector<HTMLElement>('[data-header]');
  const menuButton = document.querySelector<HTMLButtonElement>('[data-menu-button]');
  const mobileNav = document.querySelector<HTMLElement>('[data-mobile-nav]');
  const modelButton = document.querySelector<HTMLButtonElement>('[data-model-button]');
  const modelMenu = document.querySelector<HTMLElement>('[data-model-menu]');
  const modelName = document.querySelector<HTMLElement>('[data-model-name]');
  const modelProvider = document.querySelector<HTMLElement>('[data-model-provider]');
  const pandaResponse = document.querySelector<HTMLElement>('[data-panda-response]');
  const typingLine = document.querySelector<HTMLElement>('[data-typing-line]');
  const cycleModelButton = document.querySelector<HTMLButtonElement>('[data-cycle-model]');
  const consoleModel = document.querySelector<HTMLElement>('[data-console-model]');
  const consoleRoute = document.querySelector<HTMLElement>('[data-route-primary]');
  const commandDetail = document.querySelector<HTMLElement>('[data-command-detail]');
  const commandViewMap = new Map<string, (typeof commandViews)[number]>(
    commandViews.map((view) => [view.key, view]),
  );

  let consoleModelIndex = 0;
  let responseTimer: number | undefined;

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

  const closeModelMenu = () => {
    if (!modelButton || !modelMenu) return;
    modelButton.setAttribute('aria-expanded', 'false');
    modelMenu.hidden = true;
  };

  if (modelButton && modelMenu) {
    modelButton.setAttribute('aria-expanded', 'false');
    modelButton.addEventListener('click', (event) => {
      event.stopPropagation();
      const opening = modelButton.getAttribute('aria-expanded') !== 'true';
      modelButton.setAttribute('aria-expanded', String(opening));
      modelMenu.hidden = !opening;
    });

    modelMenu.querySelectorAll<HTMLButtonElement>('[data-model]').forEach((option) => {
      option.addEventListener('click', () => {
        if (modelName) modelName.textContent = option.dataset.model || 'Auto Router';
        if (modelProvider) modelProvider.textContent = option.dataset.provider || 'OpenRouter';
        closeModelMenu();

        if (!pandaResponse || !typingLine) return;
        window.clearTimeout(responseTimer);
        pandaResponse.classList.add('is-loading');
        typingLine.classList.add('active');
        responseTimer = window.setTimeout(() => {
          typingLine.classList.remove('active');
          pandaResponse.classList.remove('is-loading');
        }, 620);
      });
    });
  }

  document.addEventListener('click', (event) => {
    if (
      modelMenu &&
      modelButton &&
      event.target instanceof Node &&
      !modelMenu.contains(event.target) &&
      !modelButton.contains(event.target)
    ) {
      closeModelMenu();
    }
  });

  document.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape') return;
    closeModelMenu();
    closeMobileMenu();
  });

  if (cycleModelButton && consoleModel && consoleRoute) {
    cycleModelButton.addEventListener('click', () => {
      consoleModelIndex = (consoleModelIndex + 1) % modelOptions.length;
      const selected = modelOptions[consoleModelIndex];
      const updateModel = () => {
        consoleModel.textContent = selected.slug;
        consoleRoute.textContent = selected.route;
      };
      const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

      if (reduceMotion || typeof consoleModel.animate !== 'function') {
        updateModel();
        return;
      }

      consoleModel
        .animate(
          [
            { opacity: 1, transform: 'translateY(0)' },
            { opacity: 0, transform: 'translateY(-5px)' },
          ],
          { duration: 150, easing: 'ease-in', fill: 'forwards' },
        )
        .finished.then(() => {
          updateModel();
          consoleModel.animate(
            [
              { opacity: 0, transform: 'translateY(5px)' },
              { opacity: 1, transform: 'translateY(0)' },
            ],
            { duration: 220, easing: 'ease-out', fill: 'forwards' },
          );
        })
        .catch(updateModel);
    });
  }

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
      renderCommandDetail(row.dataset.command || 'model');
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
