# unpm

A simpler package manager for no-build websites.

## _Another_ package manager?

For a lot of websites, using npm to manage dependencies is like picking up your groceries in a semi truck.

Why? Modern package managers are complicated. They use a complex algorithm to make sure shared transitive dependency versions work out, maybe do some fancy filesystem stuff to save disk space. Installation is non-deterministic, so they write out a special lock file just to reliably install the same dependencies. And of course, without another tool to bundle everything up, you can't even use the installed files in a browser.

**unpm is different**: a package manager built on modern web technologies, specifically for websites with no build step.

- Rather than a bespoke configuration format, you use an [import map](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/script/type/importmap): a new browser feature that lets you customize module names.
- Rather than downloading packages only from special repositories, unpm can download dependencies from any website.
- Rather than using a special command to modify your dependencies, unpm lets you just edit the files.
- Rather than requiring you to use a compiler and bundler, unpm downloads files that you can serve directly with no build step.
- Rather forcing you to install your dependencies over and over, unpm is designed for you to just commit them to your repo.

## Getting started

After installing unpm, create an `unpm.json` file at the root of your project:

```json
{
  "imports": {
    "preact": "https://esm.sh/preact@10.19.3",
  }
}
```

Your first reaction might be "that looks like an import map". And you'd be right: `unpm.json` files are also valid import maps!

Unlike other package managers, unpm lets you install packages from any website. We recommend [esm.sh](https://esm.sh), but any website will work — [cdnjs](https://cdnjs.com), GitHub raw links, even your own personal website!

Once you've filled out `unpm.json` with all your dependencies, run `unpm vendor` to download them locally:

```sh
unpm vendor
```

It will create a `vendor` folder that looks something like this:

```
vendor/
├── esm.sh/
│   └── [all esm.sh dependencies go here]
├── importmap.js
├── importmap.json
└── jsconfig.json
```

**Do not add this folder to your .gitignore!** With unpm, your dependencies are meant to be _vendored_ — committed to your source control.

Next, load `importmap.js` in your HTML files:

```html
<script src="/vendor/importmap.js"></script>
```

Two important notes:

1. `importmap.js` **must not be a module** (no `type="module"` on the script tag)
2. `importmap.js` **must be loaded before any modules**

That's it! Your dependencies will now work unbundled in a browser.

## Types

unpm can also download TypeScript type definitions from supporting websites. If a response includes the header `x-typescript-types`, unpm will download its type definitions.

You'll need to tell TypeScript where to find any vendored type definitions. You can do that by having your `jsconfig.json` extend the one that unpm generates:

```json
{
  "extends": ["./vendor/jsconfig.json"]
}
```

## CLI

The unpm command line interface is small: just three commands. Note that there are no commands for adding, removing or updating packages; instead, you should edit `unpm.json` directly and then run `unpm vendor`.

### vendor

`unpm vendor` downloads all the modules listed in `unpm.json`. It recreates the output directory from scratch, removing any files that are no longer needed. Pinned files are preserved.

### check

`unpm check` finds any issues with the vendored modules:

- Any module specifiers within vendored files that aren't present in the `unpm.json` import map (error)
- Any `unpm.json` import map entries that point to files that don't exist (error)
- Any vendored files that aren't reachable from any `unpm.json` import map entries (warning)

If an error is found, `unpm check` will exit with code 1.

### why

Given a vendored file on disk, `unpm why` shows all paths from an `unpm.json` import map entry to that file.

## unpm.json

unpm's configuration file is called `unpm.json`. It's also a valid import map — a guiding principle of unpm is that if you decide to stop using it, you should be able to simply use the contents of `unpm.json` itself as your import map proper, and your website will continue to work unaffected.

Note that while all `unpm.json` files are import maps, the converse is not true: `unpm.json` supports only a subset of the import map spec.

`unpm.json` supports two top-level keys: `imports` and `unpm`. Here's an example `unpm.json` file:

```json
{
  "imports": {
    "preact": "https://esm.sh/preact@10.19.3",
    "validate": "https://raw.githubusercontent.com/jakelazaroff/validate.js/refs/heads/main/validate.js"
  },
  "unpm": {
    "out": "./src/vendor"
  }
}
```

### imports

`imports` is a [module specifier map](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/script/type/importmap#module_specifier_map) that maps between module specifiers and URLs. It's a bit stricter than actual import maps: you can only use ["bare modules"](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/script/type/importmap#bare_modules), meaning each property must resolve to a JavaScript file rather than a directory. In addition, each value must be a full URL.

Unlike most package managers, unpm doesn't rely on an external package repository like npm or JSR. You can install packages from any URL on the Internet.

### unpm

`unpm` is a map that holds unpm-specific configuration. All options are available as command line flags as well, but when repeatedly running commands it can be convenient to configure them from `unpm.json`.

- `out` specifies the directory to write any output files. Defaults to `./vendor`.
- `root` specifies the path at which the output files are available on your website. Defaults to `/vendor`.
- `pin` is a string array of glob patterns matching file paths relative to the output directory. Any matching files won't be removed or updated when running `unpm vendor`. Pinning is mostly useful when you've made local changes to a dependency that you don't want to be overwritten.

To use `unpm.json` directly as an import map, remove the `unpm` key (browsers ignore unknown import map keys so it won't break anything, but there's no reason to keep it) and paste the rest into a `<script type="importmap">` in your HTML.

## FAQ

These aren't actually frequently asked; just socratic explanations:

### Don't real web applications need a build step?

Not at all! JavaScript has gotten really good; these days, you can get a similar developer experience without building anything. [Multiple](https://preactjs.com/guide/v10/no-build-workflows/) [major](https://github.com/solidjs/solid/blob/main/packages/solid/html/README.md) [frameworks](https://vuejs.org/guide/extras/ways-of-using-vue.html#standalone-script) have documentation on no-build setups.

### Why do I need a package manager if I don't have a build step?

You might not! You could hotlink to CDNs like esm.sh, or download the files yourself.

In practice, though, there are a bunch of problems unpm solves beyond just downloading the files for you:

- If a library has multiple files, you'd need to vendor all of them and preserve the directory structure.
- If a library is written in TypeScript, you'd need to find a transpiled version.
- If you're checking types, you'd need to find the type definitions and configure TypeScript.
- If a transitive dependency is missing from your import map, you won't know until you actually load your website.

### Isn't it bad to commit dependencies to my repo?

There are two main reasons most package managers advise you not to commit dependencies:

- Dependencies with native binaries will only work on a single platform.
- The folder can be really big — hundreds or even thousands of megabytes of code.

That's it! It's true that both of these are fixed by not committing dependencies to your repository. But it introduces a ton of drawbacks:

- Since your dependencies are not committed to source control, you depend on an external system to build and run your app.
- Since installation across version ranges is non-deterministic, they need a lock file to make sure the exact same dependencies get installed.
- Since you can't just edit a dependency file, they need [baroque workarounds](https://pnpm.io/cli/patch) to let you patch a dependency.
- Since you can't use the installed files in your browser, you need _another_ tool to bundle everything together.

For some apps, this might be unavoidable. But for many websites, these are tradeoffs that don't need to be made. For example, native binaries aren't an issue for websites, because code that runs in browsers is not platform specific. And the solution to a bloated dependencies folder is to download less code (and, ideally, use fewer dependencies in the first place).

If you're still unconvinced, htmx has [a great essay on vendoring dependencies](https://htmx.org/essays/vendoring/).
