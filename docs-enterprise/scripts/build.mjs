import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import AdmZip from 'adm-zip';
import * as pagefind from 'pagefind';

const root = fileURLToPath(new URL('../', import.meta.url));
process.chdir(root);
const source = path.join(root, '.build-source');
const output = path.join(root, 'build');
const products = ['oss', 'enterprise', 'central', 'vmagent', 'cloud-connectors'].filter((name) =>
  fs.existsSync(path.join(root, name)),
);
const archive = path.join(source, 'export.zip');
// Only for iterating on the browser runtime; normal and CI builds always export
// current MDX. Never use this option after changing documentation sources.
if (!process.argv.includes('--reuse-export')) {
  fs.rmSync(source, { recursive: true, force: true });
  fs.mkdirSync(source);
  for (const dir of [...products, 'snippets', 'images', 'logo'])
    fs.cpSync(dir, path.join(source, dir), { recursive: true });
  fs.copyFileSync('docs.json', path.join(source, 'docs.json'));
  const result = spawnSync(
    path.join(root, 'node_modules/.bin/mint'),
    ['export', '--output', archive, '--disable-openapi', '--telemetry=false'],
    { cwd: source, stdio: 'inherit' },
  );
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status || 1);
}
fs.rmSync(output, { recursive: true, force: true });
fs.mkdirSync(output);
const zip = new AdmZip(archive);
let pages = 0;
for (const entry of zip.getEntries()) {
  const name = entry.entryName;
  const segments = name.split('/');
  if (segments.includes('..') || path.isAbsolute(name))
    throw new Error(`Invalid archive path: ${name}`);
  // The export also carries raw snippets and a public demo server. Only compiled
  // pages and their runtime assets belong in the authenticated production image.
  const isPage =
    name === 'index.html' || (products.includes(segments[0]) && name.endsWith('/index.html'));
  const isAsset =
    name.startsWith('_next/static/') ||
    ['images', 'logo', 'favicons'].includes(segments[0]) ||
    /^snippets\/shared\/backends\/images\/[^/]+\.(png|svg|jpg|jpeg|webp|gif)$/.test(name);
  if (entry.isDirectory || (!isPage && !isAsset)) continue;
  const target = path.join(output, name);
  fs.mkdirSync(path.dirname(target), { recursive: true });
  if (isPage) {
    // The standalone site does not expose the export's source-format endpoints.
    const html = entry
      .getData()
      .toString('utf8')
      .replace(/<link\b[^>]*>/g, (tag) =>
        /\brel="alternate"/.test(tag) &&
        (/\btype="text\/markdown"/.test(tag) || /\bhref="\/sitemap\.xml"/.test(tag))
          ? ''
          : tag,
      );
    if (!html.includes('<html') || !html.includes('</head>'))
      throw new Error(`Invalid exported page: ${name}`);
    fs.writeFileSync(
      target,
      html.replace(
        '</head>',
        '<link rel="stylesheet" href="/docs-runtime.css"><script defer src="/docs-runtime.js"></script></head>',
      ),
    );
    pages++;
  } else fs.writeFileSync(target, entry.getData());
}
const expected = products.flatMap((product) =>
  fs
    .readdirSync(path.join(root, product), { recursive: true })
    .filter((name) => name.endsWith('.mdx'))
    .map((name) => product + '/' + name.slice(0, -4)),
);
for (const route of expected)
  if (!fs.existsSync(path.join(output, route, 'index.html')))
    throw new Error(`Export omitted ${route}; add it to docs.json (a hidden group is supported).`);
// Expand Mintlify's legacy wildcard redirects into a finite lookup table. The
// Go server applies these only after authenticating, preserving query strings.
const redirects = {};
const routes = expected.map((route) => '/' + route);
for (const { source, destination } of JSON.parse(fs.readFileSync('docs.json')).redirects) {
  if (source.endsWith('/:slug*')) {
    const prefix = destination.replace(':slug*', '');
    for (const route of routes)
      if (route.startsWith(prefix))
        redirects[source.replace(':slug*', route.slice(prefix.length))] = route;
  } else redirects[source] = destination;
}
for (const destination of Object.values(redirects))
  if (!routes.includes(destination))
    throw new Error(`Redirect destination does not exist: ${destination}`);
fs.writeFileSync(path.join(output, 'redirects.json'), JSON.stringify(redirects));
fs.writeFileSync(
  path.join(output, 'llms.txt'),
  '# Odigos Enterprise documentation\n\nAccess requires an unexpired enterprise docs session.\n\n' +
    expected
      .map((route) => {
        const mdx = fs.readFileSync(route + '.mdx', 'utf8');
        const title = mdx.match(/^title:\s*["']?(.*?)["']?\s*$/m)?.[1] || route;
        return `- [${title}](https://enterprise.docs.odigos.io/${route})`;
      })
      .join('\n') +
    '\n',
);
for (const name of ['docs-runtime.js', 'docs-runtime.css'])
  fs.copyFileSync(path.join('assets', name), path.join(output, name));
const { index } = await pagefind.createIndex({ rootSelector: '#content-area' });
const indexed = await index.addDirectory({
  path: output,
  glob: `{${products.join(',')}}/**/*.html`,
});
if (indexed.errors.length) throw new Error(JSON.stringify(indexed.errors));
const written = await index.writeFiles({ outputPath: path.join(output, 'pagefind') });
if (written.errors.length) throw new Error(JSON.stringify(written.errors));
await pagefind.close();
console.log(`Built ${pages} HTML pages; all ${expected.length} source pages are present.`);
