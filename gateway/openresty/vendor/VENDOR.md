# Vendored Lua libraries

These pure-Lua libraries are copied into the repo instead of being installed when the image is built. There are two reasons:

1. The image builds offline and always uses the same versions.
2. One of them, tinyyaml, carries a local fix.

`vendor/lua/` is on `lua_package_path`.

| Library | Version | Files | License | Used for |
|---|---|---|---|---|
| [lua-resty-http](https://github.com/ledgetech/lua-resty-http) | v0.18.0 | `resty/http.lua`, `resty/http_headers.lua`, `resty/http_connect.lua` | BSD-2-Clause | Calls to etcd, Consul and Nacos |
| [jsonschema](https://github.com/api7/jsonschema) | v0.9.13 | `jsonschema.lua`, `jsonschema/store.lua` | Apache-2.0 | Validating rules against `rules.schema.json` |
| [net-url](https://github.com/golgote/neturl) | v1.2-1 | `net/url.lua` | MIT | Dependency of jsonschema |
| [lua-tinyyaml](https://github.com/api7/lua-tinyyaml) | v0.4.4 + patch | `tinyyaml.lua` | MIT | Parsing YAML rules |

The license texts are in `licenses/`.

## Local patch: tinyyaml flow-style handling

[`tinyyaml.patch`](tinyyaml.patch) is the diff against upstream v0.4.4. It fixes three flow-style bugs; each made a valid rules document fail where the Go gateway (`yaml.v3`) accepts it.

1. **Untyped flow scalars.** Upstream applies YAML core-schema typing only to block-style scalars. In flow style, every value came back as a string with its quotes stripped, so `{rate: 1}` gave `"1"` and `{a: 0, b: "0"}` gave two identical strings. The patch moves the typing into `plainscalar()` and applies it to unquoted flow scalars. Quoted ones stay strings.
2. **Flow mappings in block sequences.** An entry like `- {name: a, match: {service: s}}` was taken for an inline nested hash (`- key: value`) and failed. Entries starting with `{` or `[` are now parsed as flow collections.
3. **Extra return value.** `parseflowstyle` returns the rest of the line as a second value, which `table.insert` took as a position. The call now keeps one value.

With the patch, both gateways accept and reject the same rules documents (`t/rules_test.lua`). When upgrading tinyyaml, re-apply the patch and run `t/run.sh`.

## Note: jsonschema writes defaults

api7's jsonschema writes schema `default` values into the document it validates. `resty.micro.rules` strips `default` keywords before compiling the schema. Defaults are applied when compiling the rules, as in the Go gateway. Without this, a route that sets only `timeout.read` would also get `connect: 1s` and override `defaults.timeout.connect`.
