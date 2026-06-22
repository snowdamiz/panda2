type LegalSection = {
  heading: string;
  body: string[];
};

type LegalDocument = {
  slug: string;
  title: string;
  description: string;
  eyebrow: string;
  updated: string;
  sections: LegalSection[];
};

const updated = 'June 21, 2026';

export const legalDocuments: LegalDocument[] = [
  {
    slug: 'privacy',
    title: 'Privacy Policy',
    eyebrow: 'Privacy',
    updated,
    description: 'How Panda handles Discord server data, user memory, billing data, and support records.',
    sections: [
      {
        heading: 'What Panda collects',
        body: [
          'Panda processes Discord IDs, server metadata, role and channel permissions, command inputs, assistant responses, server knowledge, user memory consent state, billing account metadata, usage counters, audit events, and support records.',
          'Panda treats Discord messages, attachments, web results, and tool outputs as untrusted content. We do not ask customers to provide AI provider credentials.',
        ],
      },
      {
        heading: 'How Panda uses data',
        body: [
          'We use data to deliver assistant responses, enforce server permissions, meter plan usage, prevent abuse, process billing, provide support, maintain security, and improve reliability.',
          'Server admins control knowledge sources, channel access, role access, memory settings, and plan-level retention.',
        ],
      },
      {
        heading: 'Retention and deletion',
        body: [
          'Conversation metadata and knowledge retention follow the active server plan unless a shorter retention setting is configured.',
          'Server owners can request export or deletion of server knowledge, user memory consent records, conversation metadata, and billing account data where deletion is legally allowed.',
        ],
      },
      {
        heading: 'Sharing',
        body: [
          'We share data with service providers only to run Panda, including infrastructure, payment processing, support, security, and managed AI or search services.',
          'We do not sell personal data. We do not publish private server content in support materials by default.',
        ],
      },
      {
        heading: 'Contact',
        body: [
          'Privacy requests, exports, deletion requests, and DPA requests can be sent through the support page. Business customers may request a signed data processing addendum.',
        ],
      },
    ],
  },
  {
    slug: 'terms',
    title: 'Terms of Service',
    eyebrow: 'Terms',
    updated,
    description: 'The customer agreement for installing, trialing, and paying for Panda in a Discord server.',
    sections: [
      {
        heading: 'Using Panda',
        body: [
          'Panda is a hosted Discord assistant sold per Discord server. The installer and Discord server owner are responsible for configuring access, permissions, billing ownership, and acceptable use inside their server.',
          'Trial access is limited by plan credits and does not automatically convert to paid access without payment approval.',
        ],
      },
      {
        heading: 'Subscriptions and billing',
        body: [
          'Paid plans are billed per server and include defined limits for AI responses, web searches, knowledge storage, schedules, retention, music, and premium tools.',
          'Payment success, failure, cancellation, upgrades, downgrades, and entitlement changes are applied only from verified billing events or Discord entitlement events.',
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
    ],
  },
  {
    slug: 'dpa',
    title: 'Data Processing Addendum',
    eyebrow: 'Business',
    updated,
    description: 'A DPA template summary for Business customers that need processor terms.',
    sections: [
      {
        heading: 'Roles',
        body: [
          'For server content, the customer is the controller and Panda acts as a processor. For account administration, billing, fraud prevention, and service security, Panda may act as an independent controller.',
          'Business customers can request a signed DPA before production rollout or renewal.',
        ],
      },
      {
        heading: 'Processing details',
        body: [
          'Subject matter: hosted Discord assistant services. Duration: subscription term plus retention and backup windows. Categories: Discord users, server admins, billing owners, support contacts, and invited members.',
          'Personal data may include Discord IDs, names visible to the bot, role and channel metadata, command content, server knowledge, memory preferences, billing metadata, audit events, and support correspondence.',
        ],
      },
      {
        heading: 'Security commitments',
        body: [
          'Panda uses access controls, tenant-scoped repository queries, audit logging, secrets management, backups, webhook verification, and entitlement checks before paid provider-spend paths.',
          'Subprocessors are limited to providers needed for hosting, payment, security, support, managed AI, search, email, and observability. A current list is available on request.',
        ],
      },
    ],
  },
  {
    slug: 'refunds',
    title: 'Refund and Cancellation Policy',
    eyebrow: 'Billing',
    updated,
    description: 'How trials, cancellations, renewals, failed payments, and refund requests work.',
    sections: [
      {
        heading: 'Trials',
        body: [
          'Trials include limited credits and never auto-convert without payment approval. Trial abuse may result in suspension across related guilds, installers, accounts, or payment methods.',
        ],
      },
      {
        heading: 'Cancellations',
        body: [
          'Canceling a subscription stops renewal at the end of the current paid period unless the billing channel requires a different effective date.',
          'Canceled servers keep export, delete, billing, and support access while paid AI, search, schedule, and premium tool access may become unavailable.',
        ],
      },
      {
        heading: 'Refunds',
        body: [
          'Refund requests are reviewed case by case. Accidental renewals, duplicate charges, and unresolved service outages are eligible for review when requested promptly.',
          'Refunds may be unavailable for excessive usage, abuse, chargebacks, violations of acceptable use, or fees controlled by the payment channel.',
        ],
      },
    ],
  },
  {
    slug: 'acceptable-use',
    title: 'Acceptable Use Policy',
    eyebrow: 'Safety',
    updated,
    description: 'Rules for safe, lawful, and reliable use of Panda.',
    sections: [
      {
        heading: 'Not allowed',
        body: [
          'Do not use Panda for spam, harassment, hate, illegal content, credential theft, malware, privacy invasion, unauthorized surveillance, or automated mass messaging.',
          'Do not attempt to bypass quotas, billing, entitlement checks, role restrictions, tool confirmations, rate limits, or Discord platform rules.',
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
        heading: 'Enforcement',
        body: [
          'We may rate limit, disable tools, suspend a guild, revoke trial credits, require owner verification, or terminate access when usage creates risk for users, Panda, Discord, or service providers.',
        ],
      },
    ],
  },
  {
    slug: 'support',
    title: 'Support',
    eyebrow: 'Help',
    updated,
    description: 'How to get help with billing, setup, permissions, usage, export, deletion, and incidents.',
    sections: [
      {
        heading: 'Where to start',
        body: [
          'Use /billing in Discord for plan, renewal, quota, checkout, and portal actions. Use /admin status for server setup, permissions, usage, web search, memory, and degraded-state checks.',
          'Paid customers can contact support for billing, permissions, export, deletion, security, and outage questions.',
        ],
      },
      {
        heading: 'Support bundles',
        body: [
          'Support may request a bundle with guild ID, plan, subscription state, quota usage, command failure counts, recent error codes, queue depth, and Discord permission gaps.',
          'Support bundles do not include raw prompts, raw Discord messages, hidden internal tools, provider model names, API keys, or billing secrets by default.',
        ],
      },
      {
        heading: 'Response expectations',
        body: [
          'Business customers receive priority handling. Other paid plans are handled in order of severity and receipt. Trial support focuses on installation, billing, and basic setup blockers.',
        ],
      },
    ],
  },
  {
    slug: 'status',
    title: 'Status',
    eyebrow: 'Reliability',
    updated,
    description: 'The public entry point for Panda incident and availability updates.',
    sections: [
      {
        heading: 'Current status',
        body: [
          'All production components are expected to be operational unless an incident is posted here or announced through support.',
          'Panda monitors Discord gateway health, HTTP readiness, SQLite readiness, queue depth, billing webhooks, quota checks, managed AI availability, web search availability, and music sidecars.',
        ],
      },
      {
        heading: 'Incident communication',
        body: [
          'During an incident, Panda may degrade paid AI responses, web search, schedules, or music while leaving help, billing, export, delete, and support commands available.',
          'Incident updates will avoid raw server content, provider model names, API keys, billing secrets, and private customer details.',
        ],
      },
    ],
  },
  {
    slug: 'security',
    title: 'Security and Vulnerability Disclosure',
    eyebrow: 'Security',
    updated,
    description: 'How Panda handles security reports, secrets, tenant isolation, and abuse response.',
    sections: [
      {
        heading: 'Security contact',
        body: [
          'Report suspected vulnerabilities through the support page with "Security" in the subject. Include reproduction steps, affected guild or account IDs if relevant, and whether any data was accessed.',
          'Do not test against servers you do not own or operate. Do not extract, modify, delete, or disclose data while investigating.',
        ],
      },
      {
        heading: 'Controls',
        body: [
          'Panda keeps Discord tokens, managed AI keys, search keys, webhook secrets, and billing secrets in the deployment secret manager.',
          'Repository queries are tenant-scoped by guild, privileged changes are audited, webhooks are verified and idempotent, and paid provider-spend paths check entitlements before work begins.',
        ],
      },
      {
        heading: 'Abuse response',
        body: [
          'Panda can disable affected guilds, drain background work, suspend billing entitlements, revoke trial credits, rotate secrets, restore from backup, and preserve audit logs during an investigation.',
        ],
      },
    ],
  },
];

export const legalDocumentMap = new Map(legalDocuments.map((document) => [document.slug, document]));
