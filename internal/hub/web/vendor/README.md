# Vendored

`preact-htm.module.js` is `preact/standalone.module.js` from
[htm](https://github.com/developit/htm) 3.1.1 (npm `htm@3.1.1`), unchanged:
htm with the Preact 10 runtime and hooks it bundles, in one ES module, so
the hub's pages need no build step and no import map (an inline script the
hub's Content-Security-Policy refuses).

| File | sha256 |
|---|---|
| `preact-htm.module.js` | `72284e8e9079c87817145df1110f74e8a2aa040b2fc384922e18dfcb46fc1fd7` |

Licenses: htm is Apache-2.0 (`LICENSE.htm`), Preact is MIT
(`LICENSE.preact`).

To update: `npm pack htm@VERSION`, copy `package/preact/standalone.module.js`
here, and update the version and hash above (`internal/hub/web_test.go`
checks the hash).
