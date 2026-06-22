import { defineConfig } from 'astro/config';
import tailwindcss from '@tailwindcss/vite';

const site = process.env.PUBLIC_SITE_URL || 'https://panda2-landing.fly.dev';

export default defineConfig({
  site,
  trailingSlash: 'never',
  devToolbar: {
    enabled: false,
  },
  vite: {
    optimizeDeps: {
      include: [
        '@solana/wallet-standard-features',
        '@wallet-standard/app',
        '@wallet-standard/features',
      ],
    },
    plugins: [tailwindcss()],
  },
});
