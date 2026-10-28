// GraphQL needs a structured error even when Central cannot be reached. The
// generic rewrite proxy aborts after 30s with an empty HTTP 500, hiding provider
// verification results. Keep this longer than verification (90s) + saving (10s).
export const GRAPHQL_PROXY_TIMEOUT_MS = 120_000;

export async function proxyGraphQL(request: Request, backendUrl: string, fetcher: typeof fetch = fetch, timeoutMs = GRAPHQL_PROXY_TIMEOUT_MS): Promise<Response> {
  const requestId = crypto.randomUUID();
  const deadline = AbortSignal.timeout(timeoutMs);
  const headers = new Headers({ 'x-request-id': requestId });
  for (const name of ['authorization', 'cookie', 'content-type', 'accept']) {
    const value = request.headers.get(name);
    if (value) headers.set(name, value);
  }

  try {
    const upstream = await fetcher(`${backendUrl.replace(/\/$/, '')}/graphql`, {
      method: 'POST',
      headers,
      body: await request.text(),
      cache: 'no-store',
      signal: AbortSignal.any([request.signal, deadline]),
    });
    return new Response(await upstream.arrayBuffer(), {
      status: upstream.status,
      headers: { 'content-type': upstream.headers.get('content-type') || 'application/json', 'x-request-id': requestId, 'cache-control': 'no-store' },
    });
  } catch {
    const timedOut = deadline.aborted;
    const code = timedOut ? 'CENTRAL_BACKEND_TIMEOUT' : 'CENTRAL_BACKEND_UNAVAILABLE';
    const message = timedOut
      ? 'Central backend did not respond before the request deadline. Provider verification may be taking too long; check Central backend logs and connectivity to the provider APIs.'
      : 'Unable to reach Central backend. Check that Central backend is running and reachable from the UI.';
    // Do not log the request body, authorization headers, or upstream exception:
    // each can contain credentials. The ID links this to backend stage logs.
    process.stderr.write(JSON.stringify({ level: 'error', message, code, requestId }) + '\n');
    return Response.json({ errors: [{ message, extensions: { code, requestId } }] }, { status: timedOut ? 504 : 502, headers: { 'x-request-id': requestId, 'cache-control': 'no-store' } });
  }
}
