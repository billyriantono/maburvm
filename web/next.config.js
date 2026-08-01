const path = require('path');

/** @type {import('next').NextConfig} */
const nextConfig = {
  output: 'standalone',
  // Keep Next.js file tracing scoped to this app. Without this, Next may infer
  // /Users/billyriantono as the workspace root because of a parent lockfile,
  // causing dev/build to scan/watch far more files and spike CPU.
  outputFileTracingRoot: path.join(__dirname),
  // rewrites() is evaluated at `next build` and baked into the route manifest,
  // so API_BASE_URL here is a BUILD-TIME value (not runtime, not NEXT_PUBLIC_*).
  // Browser requests stay same-origin; the panel hostname is only present in
  // the server-baked manifest, never in client bundles. Defaults to local dev.
  async rewrites() {
    const apiUrl = process.env.API_BASE_URL ?? 'http://localhost:8080';
    return [
      {
        source: '/api/:path*',
        destination: `${apiUrl}/api/:path*`,
      },
      {
        source: '/ws/:path*',
        destination: `${apiUrl}/ws/:path*`,
      },
      {
        source: '/install-agent.sh',
        destination: `${apiUrl}/install-agent.sh`,
      },
      {
        source: '/webhooks/:path*',
        destination: `${apiUrl}/webhooks/:path*`,
      },
    ];
  },
};

module.exports = nextConfig;