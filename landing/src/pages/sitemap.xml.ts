import type { APIRoute } from 'astro';
import { siteMeta } from '../data/landing';

export const GET: APIRoute = ({ site }) => {
  const siteUrl = site ?? new URL(siteMeta.siteUrl);
  const rootUrl = new URL('/', siteUrl).toString();
  const updatedAt = new Date().toISOString();

  return new Response(
    [
      '<?xml version="1.0" encoding="UTF-8"?>',
      '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">',
      '  <url>',
      `    <loc>${rootUrl}</loc>`,
      `    <lastmod>${updatedAt}</lastmod>`,
      '    <changefreq>weekly</changefreq>',
      '    <priority>1.0</priority>',
      '  </url>',
      '</urlset>',
      '',
    ].join('\n'),
    {
      headers: {
        'Content-Type': 'application/xml; charset=utf-8',
        'Cache-Control': 'public, max-age=3600',
      },
    },
  );
};
