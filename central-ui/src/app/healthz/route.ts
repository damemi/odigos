// Kubelet startup/liveness probe. Always 200 once Next.js is listening so the
// pod is not restarted while central-backend is still coming up.
export const dynamic = 'force-dynamic';

export async function GET() {
  return new Response(null, { status: 200 });
}
