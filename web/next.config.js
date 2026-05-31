/** @type {import('next').NextConfig} */
const nextConfig = {
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