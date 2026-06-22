import type { APIRoute } from 'astro';
import { siteMeta } from '../data/landing';
import { legalDocuments } from '../data/legal';

export const GET: APIRoute = ({ site }) => {
  const siteUrl = site ?? new URL(siteMeta.siteUrl);
  const urls = ['/', ...legalDocuments.map((document) => `/${document.slug}`)];
  const updatedAt = new Date().toISOString();

  return new Response(
    [
      '<?xml version="1.0" encoding="UTF-8"?>',
      '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">',
      ...urls.flatMap((path) => [
        '  <url>',
        `    <loc>${new URL(path, siteUrl).toString()}</loc>`,
        `    <lastmod>${updatedAt}</lastmod>`,
        '    <changefreq>weekly</changefreq>',
        `    <priority>${path === '/' ? '1.0' : '0.6'}</priority>`,
        '  </url>',
      ]),
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
