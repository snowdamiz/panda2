import { defineConfig } from 'astro/config';

const site = process.env.PUBLIC_SITE_URL || 'https://panda2-landing.fly.dev';

export default defineConfig({
  site,
  trailingSlash: 'never',
});
