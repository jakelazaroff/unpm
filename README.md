# unpm

A last-generation package manager for next-generation websites.

## Why another package manager?

unpm combines a new browser feature — [import maps](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/script/type/importmap) — with an old technique — [vendoring dependencies](https://htmx.org/essays/vendoring/), or committing dependency source code to your repository.

## Getting started

After installing unpm, create an `unpm.json` file at the root of your project:

```json
{
  "imports": {
    "preact": "https://esm.sh/preact@10.19.3"
  }
}
```

Your first reaction might be "that looks like an import map". And you'd be right: `unpm.json` files are valid import maps! A guiding principle of unpm is that if you decide to stop using it, you should be able to simply use `unpm.json` as your import map proper, and your website will continue to work unaffected.

Once you've filled out `unpm.json` with all your dependencies, run `unpm fetch` to download them locally:

```sh
unpm fetch
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

**Do not add this folder to your .gitignore!** Whereas most package managers 

Load `importmap.js` in your HTML files:

```html
<script src="/vendor/importmap.js"></script>
```

Two important notes:
1. `importmap.js` **must not be a module** (no `type="module"` on the script tag)
2. `importmap.js` **must be loaded before any modules**

Because import maps affect how modules are loaded

## unpm.json

`unpm.json` only allows a subset of the import map spec: you can only use ["bare modules"](https://developer.mozilla.org/en-US/docs/Web/HTML/Reference/Elements/script/type/importmap#bare_modules), meaning each property must resolve to a JavaScript file rather than a directory. In addition, each value must be a full URL.

```json
{
  "imports": {
    "preact": "https://esm.sh/preact@10.19.3",
    "validate": "https://raw.githubusercontent.com/jakelazaroff/validate.js/refs/heads/main/validate.js"
  }
}
```
