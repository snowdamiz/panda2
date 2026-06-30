export type LegalAction = {
  label: string;
  href: string;
  external?: boolean;
};

export type LegalFact = {
  label: string;
  value: string;
};

export type LegalSection = {
  heading: string;
  body: string[];
};

export type LegalDocument = {
  slug: string;
  navLabel: string;
  title: string;
  description: string;
  eyebrow: string;
  updated: string;
  summary: string[];
  facts: LegalFact[];
  sections: LegalSection[];
  primaryAction: LegalAction;
  secondaryAction?: LegalAction;
  relatedSlugs: string[];
};

const updated = 'June 21, 2026';

export const legalDocuments: LegalDocument[] = [
  {
    slug: 'privacy',
    navLabel: 'Privacy',
    title: 'Privacy Policy',
    eyebrow: 'Privacy',
    updated,
    description: 'How Panda handles Discord server data, user memory, billing data, and support records.',
    summary: [
      'Panda processes only the server, billing, support, and security data needed to run the Discord assistant.',
      'Server owners control knowledge sources, memory settings, channel access, role access, and pack-level retention.',
      'Privacy, export, deletion, and DPA requests start through support so they can be tied to the right server owner.',
    ],
    facts: [
      { label: 'Scope', value: 'Discord servers, wallet billing, support, and security events' },
      { label: 'Memory', value: 'Controlled by server settings and consent state' },
      { label: 'Retention', value: 'Pack based unless a shorter setting is configured' },
      { label: 'Sale of data', value: 'Panda does not sell personal data' },
    ],
    primaryAction: { label: 'Request privacy help', href: '/support' },
    secondaryAction: { label: 'Read the DPA', href: '/dpa' },
    relatedSlugs: ['terms', 'dpa', 'security'],
    sections: [
      {
        heading: 'What Panda collects',
        body: [
          'Panda processes Discord IDs, server metadata, role and channel permissions, command inputs, assistant responses, server knowledge, user memory consent state, billing account metadata, usage counters, audit events, and support records.',
          'Panda treats Discord messages, attachments, web results, and tool outputs as untrusted content. Customers do not need to provide AI provider credentials to use the hosted service.',
        ],
      },
      {
        heading: 'How Panda uses data',
        body: [
          'We use data to deliver assistant responses, enforce server permissions, meter credit usage, prevent abuse, process billing, provide support, maintain security, and improve reliability.',
          'Server admins control knowledge sources, channel access, role access, memory settings, response behavior, and pack-level retention from Discord or account surfaces.',
        ],
      },
      {
        heading: 'Server controls',
        body: [
          'Administrators can limit where Panda responds, which roles can use tools, which knowledge sources are available, and whether memory features are enabled for the server.',
          'Privileged changes, sensitive reads, entitlement decisions, and billing actions may be recorded in audit logs so server owners can review important activity.',
        ],
      },
      {
        heading: 'Retention and deletion',
        body: [
          'Conversation metadata and knowledge retention follow the active server pack unless a shorter retention setting is configured.',
          'Server owners can request export or deletion of server knowledge, user memory consent records, conversation metadata, and billing account data where deletion is legally allowed.',
        ],
      },
      {
        heading: 'Sharing',
        body: [
          'We share data with service providers only to run Panda, including infrastructure, payment processing, support, security, managed AI, search, email, and observability providers.',
          'We do not sell personal data. We do not publish private server content in support materials by default.',
        ],
      },
      {
        heading: 'Privacy requests',
        body: [
          'Privacy requests should include the relevant Discord server ID, Discord user ID, wallet address, or billing reference when available so support can verify ownership before acting.',
          'Business customers may request a signed data processing addendum and subprocessors list before production rollout.',
        ],
      },
    ],
  },
  {
    slug: 'terms',
    navLabel: 'Terms',
    title: 'Terms of Service',
    eyebrow: 'Terms',
    updated,
    description: 'The customer agreement for installing, trialing, and paying for Panda in a Discord server.',
    summary: [
      'Panda is sold per Discord server, and the installing owner is responsible for configuration, billing ownership, and local server rules.',
      'Trials do not auto-convert. Paid access starts only after Panda verifies payment or a coupon-covered order and the activation key is used in Discord.',
      'Panda may limit or suspend access for nonpayment, abuse, security risk, unlawful activity, or reliability threats.',
    ],
    facts: [
      { label: 'Customer', value: 'Discord server owner or authorized installer' },
      { label: 'Billing unit', value: 'Prepaid credit packs per Discord server' },
      { label: 'Trial conversion', value: 'No automatic paid conversion' },
      { label: 'Activation', value: 'Verified order plus Discord activation key' },
    ],
    primaryAction: { label: 'Start trial', href: '/#install' },
    secondaryAction: { label: 'Billing policy', href: '/refunds' },
    relatedSlugs: ['privacy', 'refunds', 'acceptable-use'],
    sections: [
      {
        heading: 'Using Panda',
        body: [
          'Panda is a hosted Discord assistant sold per Discord server. The installer and Discord server owner are responsible for configuring access, permissions, billing ownership, and acceptable use inside their server.',
          'You must have authority to install Panda, grant Discord permissions, configure paid access, and manage server data for the server where Panda is used.',
        ],
      },
      {
        heading: 'Trials and activation',
        body: [
          'Trial access is limited by trial credits and does not automatically convert to paid access without payment approval.',
          'Paid access is applied only after Panda verifies a server-created billing order, including a native SOL payment or a coupon-covered zero-due order, and the billing owner activates the one-time key in Discord.',
        ],
      },
      {
        heading: 'Packs and billing',
        body: [
          'Paid packs are prepaid per server. Each pack grants a fixed number of credits plus knowledge storage and retention limits. Credits are consumed by actions, and more expensive actions cost more credits.',
          'Packs do not renew automatically. The billing owner is responsible for buying additional packs, payment disputes, taxes or fees controlled by payment channels, and keeping wallet or account access available.',
        ],
      },
      {
        heading: 'Customer responsibilities',
        body: [
          'Customers are responsible for server configuration, member notices, role assignments, channel restrictions, knowledge source permissions, and reviewing assistant output before acting on it.',
          'Customers may not use Panda to bypass Discord rules, payment checks, role restrictions, tool confirmations, rate limits, or legal requirements.',
        ],
      },
      {
        heading: 'Service changes',
        body: [
          'We may change features, limits, prices, or availability with reasonable notice when required for security, compliance, reliability, vendor costs, or product improvements.',
          'We may suspend access for nonpayment, abuse, security risk, unlawful activity, or usage that threatens Panda or Discord platform reliability.',
        ],
      },
      {
        heading: 'Disclaimers',
        body: [
          'Panda can make mistakes. Customers are responsible for reviewing assistant output before relying on it for moderation, legal, medical, financial, safety, or other high-impact decisions.',
          'Panda is provided as a hosted service and is not a guarantee that Discord, payment processors, hosting providers, search providers, or managed AI services will be uninterrupted.',
        ],
      },
      {
        heading: 'Termination',
        body: [
          'A customer may stop using Panda by canceling paid access, removing the bot from Discord, and requesting export or deletion where available.',
          'Panda may terminate or restrict access when continued service would create legal, security, billing, abuse, or reliability risk.',
        ],
      },
    ],
  },
  {
    slug: 'dpa',
    navLabel: 'DPA',
    title: 'Data Processing Addendum',
    eyebrow: 'Business',
    updated,
    description: 'A DPA template summary for Business customers that need processor terms.',
    summary: [
      'For server content, the customer is generally the controller and Panda acts as a processor under documented instructions.',
      'Panda uses tenant-scoped access controls, audited privileged changes, entitlement checks, backup practices, and deployment secret management.',
      'Business customers can request a signed DPA and current subprocessors list before rollout.',
    ],
    facts: [
      { label: 'Available to', value: 'Business customers and production evaluators' },
      { label: 'Role', value: 'Processor for server content' },
      { label: 'Subprocessors', value: 'Available on request' },
      { label: 'Term', value: 'Pack term plus retention and backup windows' },
    ],
    primaryAction: { label: 'Request signed DPA', href: '/support' },
    secondaryAction: { label: 'Privacy policy', href: '/privacy' },
    relatedSlugs: ['privacy', 'terms', 'security'],
    sections: [
      {
        heading: 'Roles',
        body: [
          'For server content, the customer is the controller and Panda acts as a processor. For account administration, billing, fraud prevention, and service security, Panda may act as an independent controller.',
          'Business customers can request a signed DPA before production rollout.',
        ],
      },
      {
        heading: 'Processing details',
        body: [
          'Subject matter: hosted Discord assistant services. Duration: pack term plus retention and backup windows. Categories: Discord users, server admins, billing owners, support contacts, and invited members.',
          'Personal data may include Discord IDs, names visible to the bot, role and channel metadata, command content, server knowledge, memory preferences, billing metadata, audit events, and support correspondence.',
        ],
      },
      {
        heading: 'Customer instructions',
        body: [
          'Panda processes server content to provide the configured assistant service, follow server owner instructions, enforce permissions, support billing and security, and comply with lawful obligations.',
          'Customers are responsible for configuring Panda consistently with their own notices, internal policies, member expectations, and legal obligations.',
        ],
      },
      {
        heading: 'Security commitments',
        body: [
          'Panda uses access controls, tenant-scoped repository queries, audit logging, secrets management, backups, webhook verification, and entitlement checks before paid provider-spend paths.',
          'Access to production systems is limited to operational needs, and support workflows are designed to avoid raw prompts, raw Discord messages, API keys, and billing secrets by default.',
        ],
      },
      {
        heading: 'Subprocessors',
        body: [
          'Subprocessors are limited to providers needed for hosting, payment, security, support, managed AI, search, email, and observability. A current list is available on request.',
          'Business customers may request notice of material subprocessors changes through their support contact.',
        ],
      },
      {
        heading: 'Assistance',
        body: [
          'Panda will provide reasonable assistance for verified export, deletion, incident, and data subject requests related to server content processed by Panda.',
          'Requests should include the relevant server, account, billing, or support identifiers so Panda can verify authority before taking action.',
        ],
      },
    ],
  },
  {
    slug: 'refunds',
    navLabel: 'Refunds',
    title: 'Refund and Cancellation Policy',
    eyebrow: 'Billing',
    updated,
    description: 'How trials, prepaid packs, credit expiry, failed payments, and refund requests work.',
    summary: [
      'Trials include limited credits and never auto-convert into paid access without payment approval.',
      'Packs are prepaid and do not auto-renew; credits stay usable until they are spent or the pack expires.',
      'Refund requests are reviewed case by case, with prompt accidental purchases, duplicate charges, and unresolved outages eligible for review.',
    ],
    facts: [
      { label: 'Trial billing', value: 'No automatic conversion' },
      { label: 'Packs', value: 'Prepaid; credits expire at the end of the pack window' },
      { label: 'Review basis', value: 'Case by case with usage and payment context' },
      { label: 'Support data', value: 'Guild ID, wallet, order, or activation key helps review' },
    ],
    primaryAction: { label: 'Request billing help', href: '/support' },
    secondaryAction: { label: 'Open billing', href: '/billing' },
    relatedSlugs: ['terms', 'support', 'acceptable-use'],
    sections: [
      {
        heading: 'Trials',
        body: [
          'Trials include limited credits and never auto-convert without payment approval. Trial abuse may result in suspension across related guilds, installers, accounts, or payment methods.',
          'Trial access includes a fixed number of credits, a knowledge storage cap, and a retention window.',
        ],
      },
      {
        heading: 'Stopping and expiry',
        body: [
          'Packs are prepaid and do not renew automatically. Credits stay usable until they are spent or the pack expires, so you can simply choose not to buy another pack.',
          'When a server runs out of credits or its pack expires, it keeps export, delete, billing, and support access while paid actions pause until another pack is activated.',
        ],
      },
      {
        heading: 'Activation and failed payments',
        body: [
          'Paid access depends on successful payment verification, coupon coverage, or another supported billing path before credits are granted. Failed or unverified payments grant no credits.',
          'During billing issues, Panda may keep administrative, billing, export, deletion, and support paths available while pausing paid provider-spend features.',
        ],
      },
      {
        heading: 'Refunds',
        body: [
          'Refund requests are reviewed case by case. Accidental purchases, duplicate charges, and unresolved service outages are eligible for review when requested promptly.',
          'Refunds may be unavailable for packs whose credits have been substantially consumed, abuse, chargebacks, violations of acceptable use, or fees controlled by the payment channel.',
        ],
      },
      {
        heading: 'How to request',
        body: [
          'Contact support with the Discord server ID, wallet address, order reference, activation key, and a short description of the issue.',
          'Do not post private keys, seed phrases, API keys, billing secrets, or raw server content in a refund request.',
        ],
      },
    ],
  },
  {
    slug: 'acceptable-use',
    navLabel: 'Acceptable Use',
    title: 'Acceptable Use Policy',
    eyebrow: 'Safety',
    updated,
    description: 'Rules for safe, lawful, and reliable use of Panda.',
    summary: [
      'Panda may not be used for spam, harassment, credential theft, malware, privacy invasion, unlawful content, or unauthorized surveillance.',
      'High-impact decisions require meaningful human review, especially moderation, legal, medical, financial, employment, housing, and safety decisions.',
      'Panda may rate limit, disable tools, suspend guild access, revoke trial credits, or terminate access when usage creates risk.',
    ],
    facts: [
      { label: 'Applies to', value: 'All trials, paid servers, admins, members, and integrations' },
      { label: 'Tool use', value: 'Must respect role gates and confirmation checks' },
      { label: 'Human review', value: 'Required for high-impact decisions' },
      { label: 'Enforcement', value: 'Limits, suspension, or termination when risk requires it' },
    ],
    primaryAction: { label: 'Report abuse', href: '/support' },
    secondaryAction: { label: 'Security policy', href: '/security' },
    relatedSlugs: ['terms', 'security', 'privacy'],
    sections: [
      {
        heading: 'Not allowed',
        body: [
          'Do not use Panda for spam, harassment, hate, illegal content, credential theft, malware, privacy invasion, unauthorized surveillance, or automated mass messaging.',
          'Do not attempt to bypass credit checks, billing, entitlement checks, role restrictions, tool confirmations, rate limits, or Discord platform rules.',
        ],
      },
      {
        heading: 'Automated abuse',
        body: [
          'Do not use Panda to coordinate mass mentions, deceptive engagement, malicious social engineering, account takeover, scraping, or evasion of moderation systems.',
          'Do not connect Panda to integrations, prompts, tools, or workflows intended to hide abuse, launder instructions, or conceal the identity of the operator.',
        ],
      },
      {
        heading: 'High-impact use',
        body: [
          'Do not rely on Panda as the sole decision-maker for legal, medical, financial, employment, housing, safety, or other high-impact decisions.',
          'Moderation drafts and recommendations must be reviewed by an authorized human moderator before action is taken.',
        ],
      },
      {
        heading: 'Security testing',
        body: [
          'Do not test Panda against servers you do not own or operate. Do not attempt to access another server, billing owner, wallet account, data export, support bundle, or administrative function.',
          'Good-faith vulnerability reports should follow the security disclosure process and avoid data extraction, persistence, service disruption, or public disclosure before review.',
        ],
      },
      {
        heading: 'Enforcement',
        body: [
          'We may rate limit, disable tools, suspend a guild, revoke trial credits, require owner verification, or terminate access when usage creates risk for users, Panda, Discord, or service providers.',
          'Severe or repeated violations may also lead to preserving audit logs, blocking related accounts, or refusing future trials or paid access.',
        ],
      },
    ],
  },
  {
    slug: 'support',
    navLabel: 'Support',
    title: 'Support',
    eyebrow: 'Help',
    updated,
    description: 'How to get help with billing, setup, permissions, usage, export, deletion, and incidents.',
    summary: [
      'Start in Discord: use /billing for pack and activation details, and ask Panda for setup and permission checks.',
      'Support can help with billing, permissions, export, deletion, security, outage, and installation blockers.',
      'Support bundles should include operational identifiers and error states, not raw prompts, raw messages, secrets, or billing credentials.',
    ],
    facts: [
      { label: 'Fastest path', value: '/billing plus Panda setup chat inside Discord' },
      { label: 'Paid support', value: 'Billing, setup, export, deletion, security, and outage help' },
      { label: 'Trial support', value: 'Installation, billing, and basic setup blockers' },
      { label: 'Sensitive data', value: 'Do not send raw secrets or private keys' },
    ],
    primaryAction: { label: 'Open account', href: '/account' },
    secondaryAction: { label: 'Check status', href: '/status' },
    relatedSlugs: ['status', 'refunds', 'security'],
    sections: [
      {
        heading: 'Where to start',
        body: [
          'Use /billing in Discord for pack, credit balance, and activation status. Ask Panda for server setup, permissions, usage, web search, memory, and degraded-state checks.',
          'Paid customers can contact support for billing, permissions, export, deletion, security, and outage questions.',
        ],
      },
      {
        heading: 'Billing and account help',
        body: [
          'For billing issues, include the Discord server ID, wallet address, order reference, coupon code if relevant, activation key if available, and the visible payment or entitlement state.',
          'Never share seed phrases, private keys, API keys, bot tokens, database URLs, webhook secrets, or unmanaged billing credentials.',
        ],
      },
      {
        heading: 'Support bundles',
        body: [
          'Support may request a bundle with guild ID, pack, account status, credit usage, command failure counts, recent error codes, queue depth, and Discord permission gaps.',
          'Support bundles do not include raw prompts, raw Discord messages, hidden internal tools, provider model names, API keys, or billing secrets by default.',
        ],
      },
      {
        heading: 'Privacy and security requests',
        body: [
          'Export, deletion, DPA, and vulnerability requests should include enough identifiers to verify account or server ownership before support acts.',
          'Security reports should include reproduction steps, affected IDs if relevant, expected impact, and whether any data was accessed.',
        ],
      },
      {
        heading: 'Response expectations',
        body: [
          'Business customers receive priority handling. Other paid packs are handled in order of severity and receipt. Trial support focuses on installation, billing, and basic setup blockers.',
          'During incidents, support may direct customers to the status page while keeping billing, export, deletion, and security channels available.',
        ],
      },
    ],
  },
  {
    slug: 'status',
    navLabel: 'Status',
    title: 'Status',
    eyebrow: 'Reliability',
    updated,
    description: 'The public entry point for Panda incident and availability updates.',
    summary: [
      'All production components are expected to be operational unless an incident is posted or announced through support.',
      'Panda monitors Discord connectivity, HTTP readiness, data storage, queue health, payment verification, credit checks, managed AI, search, and music sidecars.',
      'During incidents, Panda may degrade paid features while preserving help, billing, export, delete, and support paths.',
    ],
    facts: [
      { label: 'Normal state', value: 'Operational unless an incident is posted' },
      { label: 'Watched paths', value: 'Discord, HTTP, database, queues, billing, AI, search, music' },
      { label: 'Degraded mode', value: 'Admin and support paths remain available when possible' },
      { label: 'Private data', value: 'Incident notes avoid private customer details' },
    ],
    primaryAction: { label: 'Get support', href: '/support' },
    secondaryAction: { label: 'Security reports', href: '/security' },
    relatedSlugs: ['support', 'security', 'refunds'],
    sections: [
      {
        heading: 'Current status',
        body: [
          'All production components are expected to be operational unless an incident is posted here or announced through support.',
          'Panda monitors Discord gateway health, HTTP readiness, SQLite readiness, queue depth, SOL payment verification, credit checks, managed AI availability, web search availability, and music sidecars.',
        ],
      },
      {
        heading: 'Monitored components',
        body: [
          'Core availability depends on Discord gateway events, Discord interactions, the landing and billing pages, the assistant API, queue workers, billing verification, search providers, managed AI providers, and storage.',
          'Music availability may also depend on sidecar health, stream extraction, network conditions, and Discord voice connection state.',
        ],
      },
      {
        heading: 'Incident communication',
        body: [
          'During an incident, Panda may degrade paid AI responses, web search, schedules, or music while leaving help, billing, export, delete, and support commands available.',
          'Incident updates will avoid raw server content, provider model names, API keys, billing secrets, and private customer details.',
        ],
      },
      {
        heading: 'Degraded states',
        body: [
          'When an upstream provider is unavailable, Panda may return a degraded response, queue work for later, pause spend-heavy features, or ask admins to retry after service health improves.',
          'Billing and entitlement checks remain conservative during degraded states so server owners can see credit state before paid work resumes.',
        ],
      },
    ],
  },
  {
    slug: 'security',
    navLabel: 'Security',
    title: 'Security and Vulnerability Disclosure',
    eyebrow: 'Security',
    updated,
    description: 'How Panda handles security reports, secrets, tenant isolation, and abuse response.',
    summary: [
      'Report suspected vulnerabilities through support with "Security" in the subject and enough detail to reproduce safely.',
      'Do not test against servers you do not own, disrupt service, extract data, modify data, or publicly disclose a report before review.',
      'Panda uses tenant-scoped queries, audited privileged changes, verified webhooks, server-side payment checks, and deployment secret management.',
    ],
    facts: [
      { label: 'Report channel', value: 'Support with "Security" in the subject' },
      { label: 'Testing scope', value: 'Only servers you own or operate' },
      { label: 'Secrets', value: 'Managed through deployment secret storage' },
      { label: 'Response', value: 'Triage, reproduce, fix, rotate, and preserve logs as needed' },
    ],
    primaryAction: { label: 'Report vulnerability', href: '/support' },
    secondaryAction: { label: 'Acceptable use', href: '/acceptable-use' },
    relatedSlugs: ['acceptable-use', 'privacy', 'status'],
    sections: [
      {
        heading: 'Security contact',
        body: [
          'Report suspected vulnerabilities through the support page with "Security" in the subject. Include reproduction steps, affected guild or account IDs if relevant, and whether any data was accessed.',
          'Do not test against servers you do not own or operate. Do not extract, modify, delete, or disclose data while investigating.',
        ],
      },
      {
        heading: 'Safe testing rules',
        body: [
          'Good-faith testing must avoid service disruption, persistence, social engineering, spam, credential theft, destructive actions, and access to data belonging to other servers or accounts.',
          'If you encounter private data, stop testing, avoid further access, preserve only the minimum evidence needed to explain impact, and report through support.',
        ],
      },
      {
        heading: 'Controls',
        body: [
          'Panda keeps Discord tokens, managed AI keys, search keys, Solana RPC credentials, and billing secrets in the deployment secret manager.',
          'Repository queries are tenant-scoped by guild, privileged changes are audited, Discord webhooks are verified and idempotent, SOL payment signatures are verified server-side, and paid provider-spend paths check entitlements before work begins.',
        ],
      },
      {
        heading: 'Disclosure handling',
        body: [
          'Panda triages reports by severity, reproducibility, exploitability, customer impact, and whether secrets, billing state, or server content are at risk.',
          'Fixes may include code changes, configuration changes, key rotation, entitlement review, database corrections, customer notice, or temporary feature restrictions.',
        ],
      },
      {
        heading: 'Abuse response',
        body: [
          'Panda can disable affected guilds, drain background work, suspend billing entitlements, revoke trial credits, rotate secrets, restore from backup, and preserve audit logs during an investigation.',
          'Confirmed abuse may lead to account restrictions, blocked future installs, support escalation, or additional owner verification before service is restored.',
        ],
      },
    ],
  },
];

export const legalDocumentMap = new Map(legalDocuments.map((document) => [document.slug, document]));
