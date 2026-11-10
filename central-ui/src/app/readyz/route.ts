// Kubelet readiness probe. 200 only when central-backend /readyz is reachable
// so the UI Service does not receive traffic before GraphQL/auth work.
export const dynamic = 'force-dynamic';

const CENTRAL_BACKEND_URL = process.env.CENTRAL_BACKEND_URL || 'http://central-backend:8081';

export async function GET() {
  try {
    const res = await fetch(`${CENTRAL_BACKEND_URL.replace(/\/$/, '')}/readyz`, {
      cache: 'no-store',
      signal: AbortSignal.timeout(2500),
    });
    if (res.ok) {
      return new Response(null, { status: 200 });
    }
    return new Response('not ready', { status: 503 });
  } catch {
    return new Response('not ready', { status: 503 });
  }
}
