export const siteMeta = {
  title: 'Panda - Hosted Discord assistant for busy servers',
  description:
    'Panda is a hosted Discord assistant with server plans, managed AI responses, knowledge, schedules, web search, and admin-controlled usage.',
  ogTitle: 'Panda - Hosted Discord assistant',
  ogDescription:
    'Install Panda, start a server trial, and give your community a reliable assistant with predictable usage limits.',
  repositoryUrl: 'https://github.com/snowdamiz/panda2',
  installUrl: 'https://discord.com/oauth2/authorize',
  supportUrl: '/support',
  statusUrl: '/status',
  privacyUrl: '/privacy',
  termsUrl: '/terms',
  dpaUrl: '/dpa',
  refundsUrl: '/refunds',
  acceptableUseUrl: '/acceptable-use',
  securityUrl: '/security',
  operationsUrl: 'https://github.com/snowdamiz/panda2/blob/main/OPERATIONS.md',
  siteUrl: 'https://panda2-landing.fly.dev',
  ogImagePath: '/og-image.png',
  locale: 'en_US',
  name: 'Panda',
  keywords: [
    'Discord AI assistant',
    'Discord assistant',
    'Discord server assistant',
    'Discord automation',
    'Discord knowledge bot',
    'Discord moderation assistant',
  ],
} as const;

export const navLinks = [
  { href: '#features', label: 'Features' },
  { href: '#pricing', label: 'Pricing' },
  { href: '#control', label: 'Control' },
  { href: '#privacy', label: 'Privacy' },
] as const;

export const heroMeta = ['14-day trial', 'Usage limits', 'Admin controls', 'Server knowledge'] as const;

export const planOptions = [
  {
    label: 'Starter',
    price: '$19',
    cadence: '/server/mo',
    aiResponses: '2,000 AI responses',
    searches: '100 web searches',
    storage: '100 MB knowledge',
    retention: '30 day retention',
    badge: 'Small servers',
    command: '/billing action:upgrade plan:starter',
    featured: false,
  },
  {
    label: 'Plus',
    price: '$49',
    cadence: '/server/mo',
    aiResponses: '5,000 AI responses',
    searches: '400 web searches',
    storage: '500 MB knowledge',
    retention: '90 day retention',
    badge: 'Active communities',
    command: '/billing action:upgrade plan:plus',
    featured: true,
  },
  {
    label: 'Pro',
    price: '$99',
    cadence: '/server/mo',
    aiResponses: '10,000 AI responses',
    searches: '1,000 web searches',
    storage: '2 GB knowledge',
    retention: '180 day retention',
    badge: 'Large servers',
    command: '/billing action:upgrade plan:pro',
    featured: false,
  },
  {
    label: 'Business',
    price: '$249',
    cadence: '/server/mo',
    aiResponses: '25,000 AI responses',
    searches: '2,000 web searches',
    storage: '10 GB knowledge',
    retention: '365 day retention',
    badge: 'High-volume teams',
    command: '/billing action:upgrade plan:business',
    featured: false,
  },
] as const;

export const marqueeItems = [
  'START A SERVER TRIAL',
  'UPGRADE FROM /BILLING',
  'ASK NATURALLY',
  'CONTROL EVERY TOOL',
  'SEE USAGE BEFORE CUTOFF',
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
    title: 'Managed answer quality',
    body:
      'Panda owns the AI routing, retries, and degraded states so admins configure behavior and limits instead of vendor details.',
  },
  {
    index: '03',
    title: 'Knowledge with quotas',
    body:
      'Add server knowledge deliberately, track storage by plan, and keep retention clear for admins and members.',
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
  { tool: 'billing.manage', role: 'OWNER', enabled: false },
] as const;

export const workflowSteps = [
  {
    number: '1',
    title: 'Install and start trial',
    body:
      'The installer becomes the billing owner for that server. Panda creates a trial with credits, storage, and retention already defined.',
    icon: 'mention',
  },
  {
    number: '2',
    title: 'Configure access',
    body:
      'Admins map roles, allowed channels, web search, memory consent, knowledge sources, and response behavior from Discord.',
    icon: 'nodes',
  },
  {
    number: '3',
    title: 'Answer with limits',
    body:
      'Panda checks subscription state and quota before paid work, then replies in Discord with usage counted against the server plan.',
    icon: 'check',
  },
] as const;

export const commandRows = [
  {
    key: 'behavior',
    symbol: 'B',
    command: '/admin behavior',
    description: 'Answer length and tool policy',
    order: '01',
  },
  {
    key: 'billing',
    symbol: '$',
    command: '/billing',
    description: 'Status, checkout, and portal',
    order: '02',
  },
  {
    key: 'tool',
    symbol: 'T',
    command: '/admin tool',
    description: 'Role-based tool access',
    order: '03',
  },
  {
    key: 'audit',
    symbol: 'A',
    command: '/admin audit',
    description: 'Privileged action history',
    order: '04',
  },
] as const;

export const commandViews = [
  {
    key: 'behavior',
    values: [
      ['ANSWER LENGTH', 'Standard'],
      ['TOOL POLICY', 'Confirm writes'],
      ['WEB SEARCH', 'Allowed by plan'],
    ],
    status: 'Behavior is saved for this server',
  },
  {
    key: 'billing',
    values: [
      ['PLAN', 'Starter'],
      ['AI REMAINING', '1,842'],
      ['RENEWAL', 'Monthly'],
    ],
    status: '/billing action:upgrade creates checkout',
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
    lead: 'Usage-aware',
    body: 'plan, renewal, quota, and degraded states are visible before spend runs away.',
  },
] as const;

export const privacyItems = [
  {
    icon: 'M',
    title: 'User memory',
    detail: 'Off by default',
    state: 'OPT-IN',
  },
  {
    icon: 'K',
    title: 'Server knowledge',
    detail: 'Admin-managed sources',
    state: 'CONTROLLED',
  },
  {
    icon: 'R',
    title: 'Conversation content',
    detail: 'Plan-based retention',
    state: 'EXPIRING',
  },
  {
    icon: 'C',
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
      { href: '#pricing', label: 'Pricing', external: false },
      { href: '#privacy', label: 'Privacy', external: false },
      { href: siteMeta.statusUrl, label: 'Status', external: false },
    ],
  },
  {
    title: 'LEGAL',
    links: [
      { href: siteMeta.termsUrl, label: 'Terms', external: false },
      { href: siteMeta.privacyUrl, label: 'Privacy', external: false },
      { href: siteMeta.dpaUrl, label: 'DPA', external: false },
      { href: siteMeta.refundsUrl, label: 'Refunds', external: false },
      { href: siteMeta.acceptableUseUrl, label: 'Acceptable Use', external: false },
      { href: siteMeta.securityUrl, label: 'Security', external: false },
    ],
  },
  {
    title: 'SUPPORT',
    links: [
      { href: siteMeta.installUrl, label: 'Install', external: true },
      { href: siteMeta.supportUrl, label: 'Support', external: false },
      { href: siteMeta.operationsUrl, label: 'Operator Runbook', external: true },
    ],
  },
] as const;
