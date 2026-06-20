import type { APIRoute } from 'astro';
import { siteMeta } from '../data/landing';

export const GET: APIRoute = ({ site }) => {
  const siteUrl = site ?? new URL(siteMeta.siteUrl);
  const sitemapUrl = new URL('/sitemap.xml', siteUrl).toString();

  return new Response(
    [
      'User-agent: *',
      'Allow: /',
      '',
      `Sitemap: ${sitemapUrl}`,
      '',
    ].join('\n'),
    {
      headers: {
        'Content-Type': 'text/plain; charset=utf-8',
        'Cache-Control': 'public, max-age=3600',
      },
    },
  );
};
