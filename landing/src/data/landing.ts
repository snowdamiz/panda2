export const siteMeta = {
  title: 'Panda - Hosted Discord assistant for busy servers',
  description:
    'Panda is a hosted Discord assistant with prepaid credit packs, managed AI responses, knowledge, schedules, web search, and admin-controlled usage.',
  ogTitle: 'Panda - Hosted Discord assistant',
  ogDescription:
    'Install Panda, start a server trial, and give your community a reliable assistant with prepaid credits.',
  repositoryUrl: 'https://github.com/snowdamiz/panda2',
  installUrl: '/#install',
  accountUrl: '/account',
  billingUrl: '/billing',
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
  { href: '/#features', label: 'Features' },
  { href: '/#pricing', label: 'Pricing' },
  { href: '/#control', label: 'Control' },
  { href: '/#privacy', label: 'Privacy' },
] as const;

export const heroMeta = ['14-day trial', 'Prepaid credits', 'Admin controls', 'Server knowledge'] as const;

// Prepaid credit packs. One pack = credits for one Discord server, activated with
// a SOL payment. Credits never expire mid-pack; the pack itself sets the storage
// and retention caps. Slugs match the backend pack IDs (starter/plus/pro/business).
export const packOptions = [
  {
    slug: 'starter',
    name: 'Starter Pack',
    price: '$19',
    credits: '10,000 credits',
    storage: '100 MB knowledge',
    retention: '30-day retention',
    badge: 'Small servers',
    featured: false,
  },
  {
    slug: 'plus',
    name: 'Plus Pack',
    price: '$49',
    credits: '30,000 credits',
    storage: '500 MB knowledge',
    retention: '90-day retention',
    badge: 'Active communities',
    featured: true,
  },
  {
    slug: 'pro',
    name: 'Pro Pack',
    price: '$99',
    credits: '75,000 credits',
    storage: '2 GB knowledge',
    retention: '180-day retention',
    badge: 'Large servers',
    featured: false,
  },
  {
    slug: 'business',
    name: 'Business Pack',
    price: '$249',
    credits: '220,000 credits',
    storage: '10 GB knowledge',
    retention: '365-day retention',
    badge: 'High-volume teams',
    featured: false,
  },
] as const;

export const trialPack = {
  name: 'Trial Pack',
  credits: '1,500 credits',
  storage: '25 MB knowledge',
  retention: '14-day retention',
  duration: '14 days',
} as const;

// Representative credit costs per action. Action-oriented and provider-neutral so
// buyers can estimate how far a pack goes. Expensive actions cost more credits.
export const actionCosts = [
  { action: 'AI reply', cost: '4 credits' },
  { action: 'Web search', cost: '8 credits' },
  { action: 'Image inspection', cost: '25 credits' },
  { action: 'Image generation', cost: '150 credits' },
  { action: 'YouTube summary', cost: '20 credits + 4 / min' },
  { action: 'YouTube clip', cost: '250 credits + render' },
] as const;

export const marqueeItems = [
  'START A SERVER TRIAL',
  'PAY WITH CONNECTED WALLET',
  'ASK NATURALLY',
  'CONTROL EVERY TOOL',
  'SEE CREDITS BEFORE CUTOFF',
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
    title: 'Knowledge with clear limits',
    body:
      'Add server knowledge deliberately, track storage against the active pack, and keep retention clear for admins and members.',
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
      'Admins map roles, channel restrictions, web search, memory consent, knowledge sources, and response behavior from Discord.',
    icon: 'nodes',
  },
  {
    number: '3',
    title: 'Answer with credits',
    body:
      'Panda checks the server credit balance before paid work, then replies in Discord with credits counted against the active pack.',
    icon: 'check',
  },
] as const;

export const commandRows = [
  {
    key: 'behavior',
    symbol: 'B',
    command: 'Panda set behavior',
    description: 'Answer length and tool policy',
    order: '01',
  },
  {
    key: 'billing',
    symbol: '$',
    command: '/billing',
    description: 'Status and activation',
    order: '02',
  },
  {
    key: 'tool',
    symbol: 'T',
    command: 'Panda allow tool',
    description: 'Role-based tool access',
    order: '03',
  },
  {
    key: 'audit',
    symbol: 'A',
    command: 'Panda show audit',
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
      ['WEB SEARCH', 'Allowed by role'],
    ],
    status: 'Behavior is saved for this server',
  },
  {
    key: 'billing',
    values: [
      ['PACK', 'Starter Pack'],
      ['CREDITS', '8,450 left'],
      ['STATUS', 'Active'],
    ],
    status: 'Activation key is ready for Discord',
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
    lead: 'Credit-aware',
    body: 'credit balance, reservations, and degraded states are visible before spend runs away.',
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
    detail: 'Pack-based retention',
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
      { href: '/#features', label: 'Features', external: false },
      { href: '/#pricing', label: 'Pricing', external: false },
      { href: siteMeta.accountUrl, label: 'Account', external: false },
      { href: siteMeta.billingUrl, label: 'Billing', external: false },
      { href: '/#privacy', label: 'Privacy', external: false },
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
      { href: siteMeta.installUrl, label: 'Install', external: false },
      { href: siteMeta.accountUrl, label: 'Wallet account', external: false },
      { href: siteMeta.billingUrl, label: 'Billing', external: false },
      { href: siteMeta.supportUrl, label: 'Support', external: false },
      { href: siteMeta.operationsUrl, label: 'Operator Runbook', external: true },
    ],
  },
] as const;
