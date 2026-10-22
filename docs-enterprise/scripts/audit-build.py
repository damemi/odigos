"""Check compiled docs links/assets and reject accidentally exported source files."""

from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlparse
import json
import sys

root = Path(sys.argv[1]).resolve()
redirects = json.loads((root / "redirects.json").read_text())
errors = []


class Links(HTMLParser):
    def __init__(self, page):
        super().__init__()
        self.page = page

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag in ("a", "link"):
            target = attrs.get("href")
        elif tag in ("img", "script"):
            target = attrs.get("src")
        else:
            return
        if not target or not target.startswith("/") or target.startswith("//"):
            return
        route = unquote(urlparse(target).path)
        route = redirects.get(route, route)
        if route in ("/login", "/logout", "/auth/session"):
            return
        file = root / route.lstrip("/")
        if not file.is_file() and not (file / "index.html").is_file():
            errors.append(f"{self.page}: missing {route}")


pages = list(root.rglob("*.html"))
if not pages:
    errors.append("No compiled pages")
for page in pages:
    Links(page.relative_to(root)).feed(page.read_text())
for file in root.rglob("*"):
    if file.suffix in (".mdx", ".md") or file.name in (
        "server.js",
        "package.json",
        "export.zip",
    ):
        errors.append(f"Unexpected source/demo file: {file.relative_to(root)}")
if errors:
    sys.exit("\n".join(errors))
print(
    f"Audited {len(pages)} compiled pages: internal links and assets exist; source/demo files excluded."
)
