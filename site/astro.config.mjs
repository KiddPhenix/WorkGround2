import { defineConfig } from 'astro/config';
import sitemap from '@astrojs/sitemap';

// Served from the custom domain WorkGround2.io at the site root.
export default defineConfig({
  site: 'https://WorkGround2.io',
  build: { assets: 'static' },
  integrations: [sitemap()],
});
