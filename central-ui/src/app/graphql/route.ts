import { proxyGraphQL } from '../../lib/graphql-proxy';

export const runtime = 'nodejs';
export const dynamic = 'force-dynamic';

export async function POST(request: Request): Promise<Response> {
  return proxyGraphQL(request, process.env.CENTRAL_BACKEND_URL || 'http://central-backend:8081');
}
