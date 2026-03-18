import type { NextConfig } from 'next';

const withBundleAnalyzer = require('@next/bundle-analyzer')({
  enabled: process.env.ANALYZE === 'true',
});

const CENTRAL_BACKEND_URL = process.env.CENTRAL_BACKEND_URL || 'http://central-backend:8081';

const nextConfig: NextConfig = {
  reactStrictMode: false,
  images: {
    unoptimized: true,
  },
  compiler: {
    styledComponents: true,
    removeConsole: process.env.NODE_ENV === 'production',
  },
  compress: true,
  productionBrowserSourceMaps: false,
  experimental: {
    optimizePackageImports: ['@odigos/ui-kit', '@apollo/client', 'graphql', 'zustand', 'styled-components', 'react', 'react-dom'],
  },
  turbopack: {},
  async rewrites() {
    return [
      { source: '/graphql', destination: `${CENTRAL_BACKEND_URL}/graphql` },
      { source: '/sse/:path*', destination: `${CENTRAL_BACKEND_URL}/sse/:path*` },
      { source: '/api/signin', destination: `${CENTRAL_BACKEND_URL}/api/signin` },
      { source: '/api/signout', destination: `${CENTRAL_BACKEND_URL}/api/signout` },
      { source: '/api/auth/:path*', destination: `${CENTRAL_BACKEND_URL}/api/auth/:path*` },
      { source: '/realms/:path*', destination: `${CENTRAL_BACKEND_URL}/realms/:path*` },
      { source: '/resources/:path*', destination: `${CENTRAL_BACKEND_URL}/resources/:path*` },
    ];
  },
};

export default withBundleAnalyzer(nextConfig);
