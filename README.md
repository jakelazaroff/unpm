# unpm

A simpler package manager for no-build websites.

## Why another package manager?

Modern package managers are complicated. They use a complicated algorithm to make sure shared transitive dependency versions work out, maybe do some fancy filesystem stuff to save disk space. Because they let you specify version ranges, installation is non-deterministic — so they write out a special lock file just to reliably install the same dependencies. And of course, without another tool to bundle everything up, you can't even use the installed files in a browser.

unpm takes a different approach.
Rather than a bespoke configuration format, it uses [import maps](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/script/type/importmap): a new browser feature that lets you customize module specifiers.
Rather than downloading packages only from special repositories, you download them from any website.
Rather than using a lockfile and forcing you to install your dependencies over and over, you just commit them to your repo.

## Getting started

After installing unpm, create an `unpm.json` file at the root of your project:

```json
{
  "imports": {
    "preact": "https://esm.sh/preact@10.19.3"
  }
}
```

Your first reaction might be "that looks like an import map". And you'd be right: `unpm.json` files are also valid import maps!

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

If you're using TypeScript to check your code, you'll need to tell it where to find any vendored type definitions. You can do that by having your `jsconfig.json` extend the one that unpm generates:

```json
{
  "extends": ["./vendor/jsconfig.json"]
}
```

## CLI

Note that there are no commands for adding or removing packages; instead, you should edit `unpm.json` directly.

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

While all `unpm.json` files are import maps, the converse is not true: `unpm.json` supports only a subset of the import map spec.

`unpm.json` supports two top-level keys: `imports` and `$unpm`.

### imports

`imports` is a [module specifier map](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/script/type/importmap#module_specifier_map) that maps between module specifiers and URLs. It's a bit stricter than actual import maps: you can only use ["bare modules"](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/script/type/importmap#bare_modules), meaning each property must resolve to a JavaScript file rather than a directory. In addition, each value must be a full URL.

```json
{
  "imports": {
    "preact": "https://esm.sh/preact@10.19.3",
    "validate": "https://raw.githubusercontent.com/jakelazaroff/validate.js/refs/heads/main/validate.js"
  }
}
```

Unlike most package managers, unpm doesn't rely on an external package repository like npm or JSR. You can install packages from any URL on the Internet.

### $unpm

`$unpm` is a map that holds unpm-specific configuration. All of these are available as command line flags as well, but when running repeated commands it can be convenient to configure them from `unpm.json`.

- `out` specifies the directory to write any output files. Defaults to `./vendor`.
- `root` specifies the path at which the output files are available on your website. Defaults to `/vendor`.
- `pin` is a string array of file paths relative to the output directory. Any files in this array won't be removed or updated when running `unpm vendor`. Pinning is mostly useful when you've made local changes to a dependency that you don't want to be overwritten.

To use `unpm.json` directly as an import map, remove the `$unpm` key — browser ignore unknown import map keys so it won't break anything, but there's no reason to keep it.

## FAQ

These aren't actually frequently asked; just socratic explanations:

### Isn't it bad to commit dependencies to my repo?

Nope! There are two main reasons most package managers advise you not to commit dependencies:

- The folder can be really big.
- Dependencies with native binaries will only work on a single platform.

The solution to the first is to download less code (and, ideally, use fewer dependencies in the first place). The second isn't an issue for websites because code that runs in browsers is not platform specific.

Most package managers add a ton of overhead just to get back what vendoring gives you for free:

- Since your dependencies are not committed to source control, you depend on an external system to build and run your app.
- Since installation across version ranges is non-deterministic, they need a lock file to make sure the exact same dependencies get installed.
- Since you can't just edit a dependency file, they need [baroque workarounds](https://pnpm.io/cli/patch) to let you patch a dependency.
- Since you can't use the installed files in your browser, you need _another_ tool to bundle everything together.

If you're still unconvinced, htmx has [a great essay on vendoring dependencies](https://htmx.org/essays/vendoring/).

### Don't real web applications need a build step?

Not at all! JavaScript has gotten really good; these days, you can get a similar developer experience without building anything. [Multiple](https://preactjs.com/guide/v10/no-build-workflows/) [major](https://github.com/solidjs/solid/blob/main/packages/solid/html/README.md) [frameworks](https://vuejs.org/guide/extras/ways-of-using-vue.html#standalone-script) have documentation on no-build setups.

### Why do I need a package manager if I don't have a build step?

You might not! You could hotlink to CDNs like esm.sh, or download the files yourself.

In practice, though, there are a bunch of problems unpm solves beyond just downloading the files for you:

- If a library has multiple files, you'd need to vendor all of them and preserve the directory structure.
- If a library is written in TypeScript, you'd need to find a transpiled version.
- If you're checking types, you'd need to find the type definitions and configure TypeScript.
- If a transitive dependency is missing from your import map, you won't know until you actually load your website.