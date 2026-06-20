export const siteMeta = {
  title: 'Panda — Discord intelligence, your way',
  description:
    'Panda is an open-source Discord assistant that lets your server choose its LLM through OpenRouter.',
  ogTitle: 'Panda — Your server, with a better brain',
  ogDescription:
    'A Discord-native LLM assistant with model choice, governed tools, private memory, and operational control.',
  repositoryUrl: 'https://github.com/snowdamiz/panda2',
  setupUrl: 'https://github.com/snowdamiz/panda2#local-development',
  commandsUrl: 'https://github.com/snowdamiz/panda2#commands',
  architectureUrl:
    'https://github.com/snowdamiz/panda2/blob/main/PLAN.md#safety-privacy-and-abuse-controls',
  operationsUrl: 'https://github.com/snowdamiz/panda2/blob/main/OPERATIONS.md',
  siteUrl: 'https://panda2-landing.fly.dev',
  ogImagePath: '/og-image.svg',
  locale: 'en_US',
  name: 'Panda',
  keywords: [
    'Discord AI assistant',
    'Discord LLM bot',
    'OpenRouter Discord bot',
    'self-hosted Discord bot',
    'open-source AI assistant',
    'Discord automation',
  ],
} as const;

export const navLinks = [
  { href: '#features', label: 'Features' },
  { href: '#models', label: 'Models' },
  { href: '#control', label: 'Control' },
  { href: '#privacy', label: 'Privacy' },
] as const;

export const heroMeta = ['Go', 'Discord', 'OpenRouter', 'SQLite'] as const;

export const modelOptions = [
  {
    label: 'Auto Router',
    provider: 'Best-fit routing',
    slug: 'openrouter/auto',
    route: 'AUTO ROUTER',
    badge: 'Recommended',
  },
  {
    label: 'Claude',
    provider: 'Anthropic via OpenRouter',
    slug: 'anthropic/<model>',
    route: 'ANTHROPIC MODEL',
    badge: '',
  },
  {
    label: 'GPT',
    provider: 'OpenAI via OpenRouter',
    slug: 'openai/<model>',
    route: 'OPENAI MODEL',
    badge: '',
  },
  {
    label: 'Gemini',
    provider: 'Google via OpenRouter',
    slug: 'google/<model>',
    route: 'GOOGLE MODEL',
    badge: '',
  },
  {
    label: 'Grok',
    provider: 'xAI via OpenRouter',
    slug: 'x-ai/<model>',
    route: 'XAI MODEL',
    badge: '',
  },
] as const;

export const marqueeItems = [
  'CHOOSE ANY MODEL',
  'ASK NATURALLY',
  'CONTROL EVERY TOOL',
  'REMEMBER BY CONSENT',
] as const;

export const featureCards = [
  {
    index: '01',
    title: 'Discord-native context',
    body:
      'Ask in a channel, reply to a message, or keep a long conversation inside a thread. Panda meets your community where it already works.',
  },
  {
    index: '02',
    title: 'Reliable model routing',
    body:
      'Set a primary model and an ordered fallback list, with retries and a circuit breaker when providers have a bad day.',
  },
  {
    index: '03',
    title: 'Knowledge that belongs to you',
    body:
      'Add server knowledge deliberately, search it locally, and enrich it with embeddings only when you choose to.',
  },
  {
    index: '04',
    title: 'Tools with guardrails',
    body:
      'Server-wide policies set the ceiling. Role permissions narrow access further. Sensitive or destructive actions require a reviewed confirmation.',
  },
] as const;

export const policyRows = [
  { tool: 'web.search', role: 'MEMBERS', enabled: true },
  { tool: 'thread.summarize', role: 'MODS', enabled: true },
  { tool: 'admin.remove', role: 'ADMINS', enabled: false },
] as const;

export const consoleFallbacks = [
  { number: '01', label: 'AUTO ROUTER', state: 'PRIMARY', primary: true },
  { number: '02', label: 'FALLBACK_01', state: 'STANDBY', primary: false },
  { number: '03', label: 'FALLBACK_02', state: 'STANDBY', primary: false },
] as const;

export const consoleSettings = [
  { label: 'TEMPERATURE', value: '0.4', meterClass: 'value-40' },
  { label: 'MAX RESPONSE', value: '2,000', meterClass: 'value-68' },
  { label: 'TOOL POLICY', value: 'READ + SAFE WRITE', meterClass: 'value-82' },
] as const;

export const workflowSteps = [
  {
    number: '1',
    title: 'Ask naturally',
    body:
      'Say “Panda” in a normal message or use a context menu when you want a focused summary or explanation.',
    icon: 'mention',
  },
  {
    number: '2',
    title: 'Gather approved context',
    body:
      'Panda resolves permissions, fetches only allowed context, and runs tools within your server’s policy.',
    icon: 'nodes',
  },
  {
    number: '3',
    title: 'Get the answer',
    body:
      'Your selected model responds in Discord, with long tasks queued safely and fallbacks ready when needed.',
    icon: 'check',
  },
] as const;

export const commandRows = [
  {
    key: 'model',
    symbol: '⌁',
    command: '/admin model',
    description: 'Model, fallbacks, generation',
    order: '01',
  },
  {
    key: 'prompt',
    symbol: '¶',
    command: '/admin prompt',
    description: 'Server-level instructions',
    order: '02',
  },
  {
    key: 'tool',
    symbol: '◇',
    command: '/admin tool',
    description: 'Role-based tool access',
    order: '03',
  },
  {
    key: 'audit',
    symbol: '≡',
    command: '/admin audit',
    description: 'Privileged action history',
    order: '04',
  },
] as const;

export const commandViews = [
  {
    key: 'model',
    values: [
      ['PRIMARY', 'openrouter/auto'],
      ['FALLBACKS', '2 models'],
      ['TOOL POLICY', 'read + safe write'],
    ],
    status: 'Saved for this server',
  },
  {
    key: 'prompt',
    values: [
      ['SYSTEM OVERLAY', 'Community assistant'],
      ['STYLE', 'concise'],
      ['LAST EDITED', 'today'],
    ],
    status: 'Prompt version is active',
  },
  {
    key: 'tool',
    values: [
      ['TOOL', 'web.search'],
      ['ALLOWED ROLE', 'Members'],
      ['AUDIT', 'enabled'],
    ],
    status: 'Role policy is enforced',
  },
  {
    key: 'audit',
    values: [
      ['EVENTS', '24 recent'],
      ['SENSITIVE READS', '3 reviewed'],
      ['RETENTION', '90 days'],
    ],
    status: 'Audit trail is healthy',
  },
] as const;

export const controlPoints = [
  {
    number: '01',
    lead: 'Role-aware',
    body: 'permissions resolve from owner to channel policy.',
  },
  {
    number: '02',
    lead: 'Auditable',
    body: 'configuration and sensitive context reads leave a trail.',
  },
  {
    number: '03',
    lead: 'Operational',
    body: 'health, metrics, queues, and degraded mode are built in.',
  },
] as const;

export const privacyItems = [
  {
    icon: '○',
    title: 'User memory',
    detail: 'Off by default',
    state: 'OPT-IN',
  },
  {
    icon: '⌗',
    title: 'Server knowledge',
    detail: 'Admin-managed sources',
    state: 'CONTROLLED',
  },
  {
    icon: '↺',
    title: 'Conversation content',
    detail: 'Configurable retention',
    state: 'EXPIRING',
  },
  {
    icon: '✓',
    title: 'Destructive actions',
    detail: 'Fresh permission checks',
    state: 'CONFIRMED',
  },
] as const;

export const footerGroups = [
  {
    title: 'PRODUCT',
    links: [
      { href: '#features', label: 'Features', external: false },
      { href: '#models', label: 'Models', external: false },
      { href: '#privacy', label: 'Privacy', external: false },
    ],
  },
  {
    title: 'PROJECT',
    links: [
      { href: siteMeta.repositoryUrl, label: 'GitHub', external: true },
      { href: siteMeta.setupUrl, label: 'Setup', external: true },
      { href: siteMeta.operationsUrl, label: 'Operations', external: true },
    ],
  },
] as const;
