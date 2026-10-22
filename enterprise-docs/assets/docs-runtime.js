// Handwritten browser behavior for the protected static site.
// The build copies this source into build/docs-runtime.js.
(() => {
  if (window.odigosDocsRuntime) return;
  window.odigosDocsRuntime = true;
  let expiryTimer;
  let deadline = 0;
  let checking;
  let locked = false;
  const lock = () => {
    if (locked) return;
    locked = true;
    document.documentElement.style.visibility = 'hidden';
    location.replace(
      '/login?redirect=' + encodeURIComponent(location.pathname + location.search + location.hash),
    );
  };
  const check = () => {
    if (deadline && performance.now() >= deadline) {
      lock();
      return Promise.resolve();
    }
    if (checking) return checking;
    const started = performance.now();
    checking = fetch('/auth/session', { cache: 'no-store', credentials: 'same-origin' })
      .then(async (response) => {
        if (!response.ok) {
          lock();
          return;
        }
        const session = await response.json();
        const remaining =
          session.expiresAt * 1000 - session.serverTimeMs - (performance.now() - started);
        if (!Number.isFinite(remaining) || remaining <= 0) {
          lock();
          return;
        }
        // Rechecking never extends the local deadline established for this session.
        deadline = Math.min(deadline || Infinity, performance.now() + remaining);
        clearTimeout(expiryTimer);
        expiryTimer = setTimeout(lock, Math.max(0, deadline - performance.now()));
      })
      .catch(lock)
      .finally(() => {
        checking = null;
      });
    return checking;
  };
  check();
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) check();
  });
  window.addEventListener('pageshow', check);

  let dialog;
  let searchModule;
  let searchTimer;
  let searchVersion = 0;
  const openSearch = () => {
    if (!dialog) {
      dialog = document.createElement('dialog');
      dialog.className = 'odigos-docs-search';
      dialog.innerHTML = `
        <form method="dialog">
          <h2>Search enterprise docs</h2>
          <button aria-label="Close search">Close</button>
        </form>
        <label for="odigos-docs-query">Search documentation</label>
        <input
          id="odigos-docs-query"
          type="search"
          autocomplete="off"
          placeholder="Search installation, instrumentation, and more"
        >
        <div class="odigos-docs-results" aria-live="polite"></div>
      `;
      document.body.append(dialog);
      const input = dialog.querySelector('input');
      const results = dialog.querySelector('.odigos-docs-results');
      input.addEventListener('input', () => {
        const version = ++searchVersion;
        clearTimeout(searchTimer);
        searchTimer = setTimeout(async () => {
          if (!input.value.trim()) {
            results.replaceChildren();
            return;
          }
          try {
            await check();
            if (locked) return;
            searchModule ||= await import('/pagefind/pagefind.js');
            const found = await searchModule.search(input.value);
            const pages = await Promise.all(
              found.results.slice(0, 12).map((result) => result.data()),
            );
            if (version !== searchVersion) return;
            results.replaceChildren();
            for (const page of pages) {
              const link = document.createElement('a');
              link.href = page.url;
              link.textContent = page.meta.title || page.url;
              results.append(link);
            }
            if (!pages.length) results.textContent = 'No matching pages. Try another search.';
          } catch {
            if (!locked) results.textContent = 'Search is unavailable. Try refreshing this page.';
          }
        }, 180);
      });
      dialog.addEventListener('click', (event) => {
        if (event.target === dialog) dialog.close();
      });
    }
    dialog.showModal();
    dialog.querySelector('input').focus();
  };
  document.addEventListener(
    'keydown',
    (event) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        event.stopImmediatePropagation();
        openSearch();
      }
    },
    true,
  );
  document.addEventListener(
    'click',
    (event) => {
      if (!(event.target instanceof Element)) return;
      if (
        event.target.closest('#search-bar-entry, #search-bar-entry-mobile, [data-odigos-search]')
      ) {
        event.preventDefault();
        event.stopImmediatePropagation();
        openSearch();
        return;
      }
      const link = event.target.closest('a[href]');
      if (
        !link ||
        event.button !== 0 ||
        event.metaKey ||
        event.ctrlKey ||
        event.shiftKey ||
        event.altKey ||
        link.target === '_blank' ||
        link.hasAttribute('download')
      )
        return;
      const target = new URL(link.href, location.href);
      if (
        target.origin === location.origin &&
        (target.pathname !== location.pathname || target.search !== location.search)
      ) {
        // Full navigation ensures the server checks authorization even if Next.js
        // has already prefetched the destination into browser memory.
        event.preventDefault();
        event.stopImmediatePropagation();
        location.assign(target.href);
      }
    },
    true,
  );
  const addToolbar = () => {
    if (document.getElementById('odigos-docs-tools')) return;
    const toolbar = document.createElement('div');
    toolbar.id = 'odigos-docs-tools';
    toolbar.innerHTML = `
      <button type="button" data-odigos-search>Search docs</button>
      <form method="post" action="/logout">
        <button type="submit">Sign out</button>
      </form>
    `;
    document.body.append(toolbar);
  };
  if (document.readyState === 'loading')
    document.addEventListener('DOMContentLoaded', addToolbar, { once: true });
  else addToolbar();
})();
