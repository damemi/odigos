# OBI, bpf2go, `GOMODCACHE`, and the module file index

## What you need

For **OBI**, Odigos clones the pinned upstream commit and runs OBI's own **bpf2go** codegen in that clone (see **`materialize-obi`** in **`odiglet/obi.mk`**), then adds a local-path **`go.mod`** replace so the freshly generated **`*_bpfel.go`** / **`.o`** are on disk before **`go build`** (the go module cache tarball does not ship them). For other eBPF deps in the module cache, **bpf2go** (via upstream **`make generate`**) can still add **`.go`** / **`.o`** files; those must be on disk before **`go build`**.

The Go command can use a **module file index** to avoid re-reading every source file on each invocation. **New `.go` files** added *after* the module was first indexed are not always reflected in that index, so `go build` can compile a package **without** the generated Go sources and fail with `undefined: Bpf…` (or similar) even though the files exist on disk.

**`go build -a`** does *not* fix that by itself: it only invalidates the **build** (compile) cache, not the module file index; see *Why this affects…* below.

## Docker / CI build (same **`Makefile`** as local)

OBI lives in **`odiglet/obi.mk`**, which **`odiglet/enterprise/Makefile`** includes. **`materialize-obi`** dispatches to one of two methods via **`OBI_SETUP`** (`pin` | `release`, default `pin`). Neither touches **`go.mod`**:

- **`materialize-obi-pin`** (`OBI_SETUP=pin`): clones the pinned OBI commit (**`OBI_COMMIT`**) into **`.obi/`** and runs OBI's **bpf2go** codegen in the clone — **`generate/all`** in-tree when docker is unavailable (e.g. inside a docker build), or **`docker-generate`** on the host. Keep **`OBI_COMMIT`** in sync with the **`go.opentelemetry.io/obi`** pseudo-version in **`go.mod`** **`require`**.
- **`materialize-obi-release`** (`OBI_SETUP=release`): downloads and verifies OBI's published **`obi-<tag>-source-generated.tar.gz`** (**`OBI_VERSION`**) and extracts it under **`.obi/`**. Keep **`OBI_VERSION`** in sync with the **`go.opentelemetry.io/obi`** release tag in **`go.mod`** **`require`**.

**`go-mod-replace`** adds the transient local-path **`go.mod`** replace; **`generate`** and **`ensure-generated`** run it (through **`go-mod`**) before downloading modules.

The **`builder`** stage of **`odiglet/enterprise/Dockerfile`** splits the build into cached layers:

1. **OBI** — copies only **`odiglet/obi.mk`** and runs **`materialize-obi`**. Rebuilt only when **`obi.mk`** changes.
2. **Generate** — copies only the **`go.mod`** / **`go.sum`** files of odiglet/enterprise and its local replaces, plus the Makefile, and runs **`generate`** (each library's upstream **`make generate`**, in parallel). Rebuilt only when module metadata or the Makefile changes.
3. **Compile** — copies the full sources and runs **`ensure-generated`** (re-applies the OBI replace, which the source **`COPY`** overwrote, and regenerates any library whose **`*_bpfel.o`** is missing from the **`/go/pkg`** cache mount), then **`compile-odiglet`**.

The image build uses the default method in **`obi.mk`**; when switching methods, update both that default and the **`go.opentelemetry.io/obi`** version in **`go.mod`** to match.

Locally: **`make materialize-obi generate compile-odiglet`** from **`odiglet/enterprise`**, optional **`OBI_SETUP=release`** (with **`OBI_VERSION=v…`**) or **`OBI_COMMIT=<sha>`**.

**Related ideas**

- [Module cache layout and immutability](https://go.dev/ref/mod#module-cache)
- [Organizing a Go module](https://go.dev/doc/modules/layout) (where generated `.go` must live to be part of a package)
- [bpf2go](https://github.com/cilium/ebpf/tree/master/cmd/bpf2go) (command that emits the `*_bpfel.go` sources and the embedded `.o`)

## Why this affects generated **`.go`** and not the **`.o`**

When generating other eBPF libraries like `go.opentelemetry.io/auto`, we simply `cd` into the modcache and run `go generate` there. Why does that work for them but not OBI?

The answer is because those libraries commit generated bpf2go files (`*_bpfel.go`). The only thing that is generated in the cache at build time are the `*.o` binary eBPF files.

The `go` command’s **module file index** (`GODEBUG` **`goindex`**) is about **which paths count as this package’s Go source** when resolving imports and building the [`build.Package`](https://pkg.go.dev/go/build#Package) (e.g. [`GoFiles`](https://pkg.go.dev/go/build#Package) from [`ImportDir`](https://pkg.go.dev/go/build#Context.ImportDir) / the loader in [`load/pkg.go`](https://github.com/golang/go/blob/master/src/cmd/go/internal/load/pkg.go)). It does **not** implement “read this blob for `//go:embed`.” Non-`.go` artifacts under the same directory are **not** listed in that set; a bpf2go **`.o`** is an ordinary file on disk in `$GOMODCACHE`, not a “package member” the index must enumerate for the compiler to open later.

**`//go:embed`** is handled when the **compiler** compiles a **`.go` file** that is already part of the build and contains an embed directive. The compiler then reads the embedded path (the `.o`, etc.) with normal **filesystem I/O** under the package’s source directory (still inside the mod cache for a dependency). So yes: the blob lives in the mod cache, and the toolchain **will** read it from there **once** that `*_bpfel.go` (or equivalent) is **selected for compilation**. The index is not a separate “fetch the `.o`” step—it’s “which `.go` files are in this package.” If a stale index **omits** a generated `*_bpfel.go`, that file is never compiled, so the compiler never runs the `//go:embed` for the `.o`; you see **`undefined: Bpf…`** from other packages that **were** built without those generated types, not a missing-embed error about the object file.

So the failure mode here is a stale list of **`.go`** files, not the index “failing to find” the **`.o`**: the `.o` is not a separate line item in the indexed Go file list; the bug is the generated **`.go`** that references it not being treated as part of the package.
