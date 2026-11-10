import { sleep } from 'k6';
import tracing from 'k6/x/tracing';

// Tunables via env (k6-operator / Deployment):
//   ENDPOINT  - OTLP gRPC host:port (default: insights headless)
//   VUS       - virtual users (also set via options / TestRun)
//   SLEEP_MS  - pause between pushes (lower = more load)
//   TENANTS   - distinct service.namespace values (default 1)
export const options = {
  vus: Number(__ENV.VUS || 5),
  duration: __ENV.DURATION || '30m',
};

const endpoint = __ENV.ENDPOINT || 'odigos-insights-headless.odigos-system.svc.cluster.local:4317';

const client = new tracing.Client({
  endpoint,
  exporter: tracing.EXPORTER_OTLP,
  tls: { insecure: true },
});

const ns = __ENV.TRACE_NAMESPACE || 'loadtest';

// Cardinality contract for this file: every value Insights turns into a
// baseline key appears below as a literal. A long run has to converge on a
// stable finding count, so nothing randomized per push may reach these keys -
// adding a service, route, table or peer is a deliberate edit.
//   D1 call edges  (service.name, span name) for every parent/child pair
//   D2 egress      server.address + server.port on CLIENT and PRODUCER spans
//   D5 libraries   instrumentation scope, pinned by the Dockerfile sed patch
//   D6 db access   db.system/namespace/collection/operation + query shape
// D3 latency and D7 payload size are histograms, so varying durations and body
// sizes costs nothing. Transaction identity is (service, namespace, entry span
// name, kind) plus http.response.status_code and rpc.grpc.status_code.

const kafka = { host: 'kafka.messaging.svc.cluster.local', port: 9092 };

const stores = {
  orders: { system: 'postgresql', namespace: 'orders', host: 'orders-db.data.svc.cluster.local', port: 5432 },
  catalog: { system: 'postgresql', namespace: 'catalog', host: 'catalog-db.data.svc.cluster.local', port: 5432 },
  billing: { system: 'postgresql', namespace: 'billing', host: 'billing-db.data.svc.cluster.local', port: 5432 },
  warehouse: { system: 'postgresql', namespace: 'warehouse', host: 'warehouse-db.data.svc.cluster.local', port: 5432 },
  sessions: { system: 'redis', namespace: '0', host: 'sessions.cache.svc.cluster.local', port: 6379 },
  profiles: { system: 'mongodb', namespace: 'profiles', host: 'profiles.data.svc.cluster.local', port: 27017 },
  search: { system: 'elasticsearch', namespace: 'products', host: 'search.data.svc.cluster.local', port: 9200 },
};

// The generator fills an unset http.response.status_code from a random pick of
// {200,201,202,204}, and that attribute is a transaction identity dimension -
// left alone it splits one endpoint into four transactions that each learn from
// a quarter of the traffic. The method is pinned for a smaller reason: it
// decides whether a request body size is emitted, which D7 histograms.
function httpEntry(service, method, route, opts) {
  opts = opts || {};
  const span = {
    service: service,
    name: method + ' ' + route,
    attributes: {
      'http.request.method': method,
      'http.response.status_code': opts.status || 200,
    },
  };
  if (opts.parentIdx !== undefined) span.parentIdx = opts.parentIdx;
  if (opts.duration) span.duration = opts.duration;
  return span;
}

function grpcEntry(service, method, opts) {
  opts = opts || {};
  const span = {
    service: service,
    name: method,
    attributes: {
      'rpc.system': 'grpc',
      'rpc.method': method,
      'rpc.grpc.status_code': opts.status || 0,
    },
  };
  if (opts.parentIdx !== undefined) span.parentIdx = opts.parentIdx;
  if (opts.duration) span.duration = opts.duration;
  return span;
}

// tracing.SEMANTICS_DB is declared by the extension but TemplatedGenerator only
// implements the HTTP one, so a span tagged "db" emits no db.* attributes and
// D6 never sees it. Everything D6 reads is set by hand here; the semantics tag
// only suppresses the HTTP attributes the scenario defaults would add.
function dbCall(service, store, operation, collection, query) {
  return {
    service: service,
    name: operation + ' ' + collection,
    attributeSemantics: tracing.SEMANTICS_DB,
    attributes: {
      'span.kind': 'client',
      'db.system': store.system,
      'db.namespace': store.namespace,
      'db.collection.name': collection,
      'db.operation.name': operation,
      'db.query.text': query,
      'server.address': store.host,
      'server.port': store.port,
    },
  };
}

// No db.collection.name on purpose: this is the path where Insights recovers
// the table set from the statement itself, so the learned shape comes out of
// its SQL parser instead of an attribute we handed it.
function dbQuery(service, store, name, query) {
  return {
    service: service,
    name: name,
    attributeSemantics: tracing.SEMANTICS_DB,
    attributes: {
      'span.kind': 'client',
      'db.system': store.system,
      'db.namespace': store.namespace,
      'db.query.text': query,
      'server.address': store.host,
      'server.port': store.port,
    },
  };
}

function publish(service, topic, parentIdx) {
  const span = {
    service: service,
    name: topic + ' publish',
    attributes: {
      'span.kind': 'producer',
      'messaging.system': 'kafka',
      'messaging.operation': 'publish',
      'messaging.destination.name': topic,
      'server.address': kafka.host,
      'server.port': kafka.port,
    },
  };
  if (parentIdx !== undefined) span.parentIdx = parentIdx;
  return span;
}

function consume(service, topic, parentIdx) {
  const span = {
    service: service,
    name: topic + ' process',
    attributes: {
      'span.kind': 'consumer',
      'messaging.system': 'kafka',
      'messaging.operation': 'process',
      'messaging.destination.name': topic,
    },
  };
  if (parentIdx !== undefined) span.parentIdx = parentIdx;
  return span;
}

// Span kind is inferred from the template unless a span sets it: a span whose
// next child is a different service becomes CLIENT, a span whose parent is a
// different service becomes SERVER. Every SERVER and CONSUMER span is a
// separate transaction with its own baselines, so the transaction count is the
// number of those spans, not the number of scenarios. A CLIENT root would make
// Insights discard the whole trace, so each scenario opens on a SERVER or
// CONSUMER span.
const scenarios = [
  {
    name: 'checkout',
    weight: 5,
    semantics: tracing.SEMANTICS_HTTP,
    spans: [
      httpEntry('checkout-api', 'POST', '/checkout', { duration: { min: 80, max: 400 } }),
      { service: 'checkout-api', name: 'authenticate', duration: { min: 20, max: 80 } },
      httpEntry('auth-service', 'POST', '/auth/verify'),
      dbCall('auth-service', stores.sessions, 'GET', 'sessions', 'GET session:?'),
      { service: 'checkout-api', name: 'get-cart', parentIdx: 0 },
      httpEntry('cart-service', 'GET', '/cart/{id}'),
      // Odigos custom instrumentation: D4 profiles the format of each argument
      // and of the return value, and only keeps literals for token-shaped
      // values (capped at 12 distinct), so fixed values here stay one member.
      {
        service: 'checkout-api',
        name: 'applyPromotion',
        parentIdx: 0,
        attributes: {
          'span.kind': 'internal',
          'code.namespace': 'com.shop.checkout.PromotionService',
          'code.function': 'apply',
          'arg.0': 'SUMMER25',
          'arg.1': '42',
          'return.value': 'applied',
        },
      },
      { service: 'checkout-api', name: 'reserve-inventory', parentIdx: 0 },
      httpEntry('inventory-service', 'POST', '/reserve'),
      dbCall('inventory-service', stores.warehouse, 'UPDATE', 'reservations', 'UPDATE reservations SET held = ? WHERE sku = ?'),
      { service: 'checkout-api', name: 'charge', parentIdx: 0 },
      httpEntry('billing-service', 'POST', '/charge'),
      dbCall('billing-service', stores.billing, 'INSERT', 'payments', 'INSERT INTO payments (order_id, amount) VALUES (?, ?)'),
      publish('checkout-api', 'order.created', 0),
      consume('notification-service', 'order.created', 13),
    ],
  },
  {
    name: 'search',
    weight: 8,
    semantics: tracing.SEMANTICS_HTTP,
    spans: [
      httpEntry('search-api', 'GET', '/search', { duration: { min: 50, max: 250 } }),
      { service: 'search-api', name: 'query-index' },
      httpEntry('search-indexer', 'POST', '/index/query'),
      dbCall('search-indexer', stores.search, 'search', 'products', '{"query":{"match":{"title":"?"}}}'),
      { service: 'search-api', name: 'enrich-results', parentIdx: 0 },
      httpEntry('catalog-service', 'GET', '/products/batch'),
      dbCall('catalog-service', stores.catalog, 'SELECT', 'products', 'SELECT * FROM products WHERE sku = ANY(?)'),
    ],
  },
  {
    name: 'orders-read',
    weight: 6,
    semantics: tracing.SEMANTICS_HTTP,
    spans: [
      httpEntry('orders-api', 'GET', '/orders/{id}', { duration: { min: 40, max: 200 } }),
      dbCall('orders-api', stores.orders, 'SELECT', 'orders', 'SELECT * FROM orders WHERE id = ?'),
      { service: 'orders-api', name: 'load-shipment', parentIdx: 0 },
      httpEntry('shipping-service', 'GET', '/shipments/{id}'),
      { service: 'orders-api', name: 'load-payment', parentIdx: 0 },
      httpEntry('billing-service', 'GET', '/payments/{id}'),
      dbCall('billing-service', stores.billing, 'SELECT', 'payments', 'SELECT * FROM payments WHERE order_id = ?'),
    ],
  },
  {
    // No HTTP semantics: the generator would otherwise stamp a random
    // http.response.status_code on these entry spans, and a gRPC transaction
    // would end up split by an HTTP status it never had.
    name: 'profile-grpc',
    weight: 4,
    semantics: null,
    spans: [
      grpcEntry('profile-gateway', 'shop.ProfileService/GetProfile', { duration: { min: 30, max: 150 } }),
      dbCall('profile-gateway', stores.sessions, 'GET', 'sessions', 'GET session:?'),
      { service: 'profile-gateway', name: 'fetch-profile', parentIdx: 0 },
      grpcEntry('user-service', 'shop.UserService/Get'),
      dbCall('user-service', stores.profiles, 'find', 'profiles', '{"find":"profiles","filter":{"_id":"?"}}'),
      { service: 'profile-gateway', name: 'fetch-preferences', parentIdx: 0 },
      grpcEntry('preferences-service', 'shop.PreferencesService/Get'),
    ],
  },
  {
    name: 'fulfillment',
    weight: 3,
    semantics: tracing.SEMANTICS_HTTP,
    spans: [
      consume('fulfillment-service', 'order.created'),
      { service: 'fulfillment-service', name: 'reserve-stock' },
      httpEntry('warehouse-service', 'POST', '/stock/reserve'),
      dbCall('warehouse-service', stores.warehouse, 'UPDATE', 'stock', 'UPDATE stock SET on_hand = on_hand - ? WHERE sku = ?'),
      { service: 'fulfillment-service', name: 'create-shipment', parentIdx: 0 },
      httpEntry('shipping-service', 'POST', '/shipments'),
      dbCall('shipping-service', stores.orders, 'INSERT', 'shipments', 'INSERT INTO shipments (order_id, carrier) VALUES (?, ?)'),
      publish('fulfillment-service', 'shipment.created', 0),
    ],
  },
  {
    // Rare and slow on purpose: gives D3 a transaction whose latency histogram
    // sits far from the request-path ones.
    name: 'reporting',
    weight: 1,
    semantics: tracing.SEMANTICS_HTTP,
    spans: [
      httpEntry('reporting-api', 'GET', '/reports/daily', { duration: { min: 400, max: 1500 } }),
      { service: 'reporting-api', name: 'build-report' },
      httpEntry('analytics-service', 'POST', '/aggregate'),
      dbQuery('analytics-service', stores.orders, 'db.healthcheck', 'SELECT 1'),
      dbQuery('analytics-service', stores.orders, 'daily-revenue', 'SELECT o.id, c.tier FROM orders o JOIN customers c ON o.customer_id = c.id WHERE o.created_at > ?'),
    ],
  },
];

function defaultsFor(namespace, semantics) {
  const defaults = {
    resource: {
      attributes: {
        'service.namespace': namespace,
        'k8s.namespace.name': namespace,
      },
    },
  };
  if (semantics) {
    defaults.attributeSemantics = semantics;
  }
  return defaults;
}

// Namespace is part of the transaction identity, so this is the one knob that
// scales resident-transaction count without touching any other key. Generators
// are per-VU, so the object count is TENANTS x scenarios x VUS.
const tenantCount = Math.max(1, Number(__ENV.TENANTS || 1));
const namespaces = [];
for (let i = 0; i < tenantCount; i++) {
  namespaces.push(tenantCount === 1 ? ns : ns + '-' + i);
}

// Built once at init rather than per iteration: the constructor resolves every
// span's parent and kind, and that cost lands in the send loop where k6 is
// already the bottleneck. Traces() still re-randomizes ids, timings and body
// sizes on each call, so reuse costs no variety.
const generators = [];
let totalWeight = 0;
for (const namespace of namespaces) {
  for (const scenario of scenarios) {
    totalWeight += scenario.weight;
    generators.push({
      gen: new tracing.TemplatedGenerator({
        defaults: defaultsFor(namespace, scenario.semantics),
        spans: scenario.spans,
      }),
      cumulativeWeight: totalWeight,
    });
  }
}

function pickGenerator() {
  const target = Math.random() * totalWeight;
  for (const entry of generators) {
    if (target < entry.cumulativeWeight) {
      return entry.gen;
    }
  }
  return generators[generators.length - 1].gen;
}

export default function () {
  client.push(pickGenerator().traces());

  const sleepMs = Number(__ENV.SLEEP_MS || 50);
  if (sleepMs > 0) {
    sleep(sleepMs / 1000);
  }
}

export function teardown() {
  client.shutdown();
}
