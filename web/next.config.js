const path = require('path');

/** @type {import('next').NextConfig} */
const nextConfig = {
  output: 'standalone',
  // Keep Next.js file tracing scoped to this app. Without this, Next may infer
  // /Users/billyriantono as the workspace root because of a parent lockfile,
  // causing dev/build to scan/watch far more files and spike CPU.
  outputFileTracingRoot: path.join(__dirname),
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
    ];
  },
};

module.exports = nextConfig;