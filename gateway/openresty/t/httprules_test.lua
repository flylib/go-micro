-- Mirrors gateway/proxy/httprules_test.go.

local httprules = require("resty.micro.httprules")
local rules = require("resty.micro.rules")
local cjson = require("cjson.safe")

local function rule(spec)
    local rs, err = httprules.compile({ spec })
    T.ok(rs, "compile: " .. tostring(err))
    return rs[1]
end

local function same(a, b)
    return cjson.encode(a) == cjson.encode(b)
end

T.test("template match", function()
    local cases = {
        { "/v1/users/{id}", "/v1/users/42", { id = "42" } },
        { "/v1/users/{id}", "/v1/users/42/x", nil },
        { "/v1/users/{id}", "/v1/users/", nil },
        { "/v1/users/{user.id}/books/{book}", "/v1/users/7/books/b1", { ["user.id"] = "7", book = "b1" } },
        { "/v1/{name=projects/*/books/*}", "/v1/projects/p1/books/b2", { name = "projects/p1/books/b2" } },
        { "/v1/{name=projects/*/books/*}", "/v1/shelves/p1/books/b2", nil },
        { "/v1/files/{path=**}", "/v1/files/a/b/c.txt", { path = "a/b/c.txt" } },
        { "/v1/files/{path=**}", "/v1/files", { path = "" } },
        { "/v1/users/{id}:activate", "/v1/users/9:activate", { id = "9" } },
        { "/v1/users/{id}:activate", "/v1/users/9", nil },
        { "/v1/users/{id}", "/v1/users/a%2Fb", { id = "a/b" } },
    }
    for _, c in ipairs(cases) do
        local hr = assert(httprules.parse(c[1]))
        local vars = httprules.match(hr, c[2])
        if c[3] == nil then
            T.eq(vars, nil, c[1] .. " ~ " .. c[2])
        else
            T.ok(vars and same(vars, c[3]), c[1] .. " ~ " .. c[2] .. ": " .. tostring(vars and cjson.encode(vars)))
        end
    end
end)

T.test("templates rejected", function()
    for _, bad in ipairs({ "v1/x", "/v1//x", "/v1/x/", "/v1/{id", "/v1/**/x", "/v1/{}", "/v1/{a=**}/x", "/v1/x:", "/v1/a=b" }) do
        T.eq(httprules.parse(bad), nil, bad .. " accepted")
    end
end)

T.test("specificity", function()
    local rs = assert(httprules.compile({
        { method = "GET", path = "/v1/**", target = "/s.S/All" },
        { method = "GET", path = "/v1/items/{id}", target = "/s.S/Get" },
        { method = "GET", path = "/v1/items/special", target = "/s.S/Special" },
        { method = "POST", path = "/v1/items/{id}", target = "/s.S/Update" },
    }))
    for path, want in pairs({ ["/v1/items/special"] = "/s.S/Special", ["/v1/items/42"] = "/s.S/Get", ["/v1/other/a/b"] = "/s.S/All" }) do
        local hr = httprules.find(rs, "GET", path)
        T.eq(hr and hr.target, want, "GET " .. path)
    end
    T.eq((httprules.find(rs, "POST", "/v1/items/1") or {}).target, "/s.S/Update", "POST")
    T.eq(httprules.find(rs, "DELETE", "/v1/items/1"), nil, "DELETE")
    T.eq(httprules.compile({
        { method = "GET", path = "/v1/x", target = "/s.S/A" },
        { method = "GET", path = "/v1/x", target = "/s.S/B" },
    }), nil, "duplicate accepted")
end)

local function build(spec, path, query, body)
    local hr = rule(spec)
    local vars = assert(httprules.match(hr, path), path)
    local out, err = httprules.build(hr, body, vars, query)
    if not out then
        return nil, err
    end
    return cjson.decode(out), out
end

T.test("build without body", function()
    local spec = { method = "GET", path = "/v1/users/{id}", target = "/u.U/Get",
        params = { id = "string", verbose = "bool", tags = "repeated", ["page.size"] = "number" } }
    local m = assert(build(spec, "/v1/users/42", { verbose = "true", tags = "a", ["page.size"] = "10", id = "999", unknown = "x" }, ""))
    T.eq(m.id, "42", "path wins over query")
    T.eq(m.verbose, true, "bool")
    T.ok(type(m.tags) == "table" and m.tags[1] == "a" and #m.tags == 1, "repeated")
    T.eq(m.page and m.page.size, "10", "nested")
    T.eq(m.unknown, nil, "param not in params")
    local _, err = build(spec, "/v1/users/1", { verbose = "yes" }, "")
    T.eq(err and err.http, 400, "bad bool")
    _, err = build(spec, "/v1/users/1", {}, '{"x":1}')
    T.eq(err and err.http, 400, "body on a no-body rule")
    T.ok(build(spec, "/v1/users/1", {}, "{}"), "empty object body refused")
end)

T.test("build without params keeps query", function()
    local m = assert(build({ method = "GET", path = "/v1/x", target = "/s.S/X" }, "/v1/x",
        { a = "1", ["b.c"] = "2", r = { "1", "2" } }, ""))
    T.eq(m.a, "1", "a")
    T.eq(m.b and m.b.c, "2", "b.c")
    T.ok(m.r and m.r[1] == "1" and m.r[2] == "2", "repeated key")
end)

T.test("build star body", function()
    local spec = { method = "POST", path = "/v1/users/{id}", target = "/u.U/Update", body = "*" }
    local m, raw = build(spec, "/v1/users/7", { ignored = "1" }, '{"id":"from-body","name":"n","big":9007199254740993,"list":[]}')
    T.ok(m, "build failed")
    T.eq(m.id, "7", "path overrides body")
    T.eq(m.name, "n", "body field")
    T.eq(m.ignored, nil, "query used with *")
    T.ok(raw:find('"big":"9007199254740993"', 1, true), "big number not kept exact: " .. raw)
    T.ok(raw:find('"list":[]', 1, true), "empty array became an object: " .. raw)
    local _, err = build(spec, "/v1/users/7", {}, "[1]")
    T.eq(err and err.http, 400, "array body")
end)

T.test("build field body", function()
    local m = assert(build({ method = "POST", path = "/v1/users/{user.id}/notes", target = "/u.U/Note", body = "note" },
        "/v1/users/3/notes", { ["user.name"] = "al" }, '{"text":"hi"}'))
    T.eq(m.note and m.note.text, "hi", "body field")
    T.eq(m.user and m.user.id, "3", "nested path var")
    T.eq(m.user and m.user.name, "al", "nested query")
end)

T.test("reply response_body keeps the text", function()
    local hr = { response_body = "payload" }
    T.eq(httprules.reply(hr, '{"payload":{"body":"YQ==","n":12345678901234567890},"username":"u"}'),
        '{"body":"YQ==","n":12345678901234567890}', "field cut")
    T.eq(httprules.reply({ response_body = "a.b" }, '{"a": {"b" : [1, {"c":"}"}] }, "z":1}'), '[1, {"c":"}"}]', "nested, tricky text")
    T.eq(httprules.reply({ response_body = "missing" }, '{"a":1}'), "null", "unset field")
    T.eq(httprules.reply({ response_body = "" }, '{"a":1}'), '{"a":1}', "whole message")
end)

T.test("forward_claims keys collected", function()
    local key = require("resty.openssl.pkey").new({ type = "RSA", bits = 2048 })
    local pub = ngx.encode_base64(key:tostring("public", "PEM"))
    local rs = assert(rules.parse([[
version: 1
routes:
  - name: a
    match: {service: a}
    plugins: [{name: jwt-auth, config: {public_key: "]] .. pub .. [[", forward_claims: {user-id: sub}}}]
http_rules:
  - {method: GET, path: "/v1/users/{id}", target: /u.U/Get}
]], "nacos", 1))
    T.ok(rs.forwarded["user-id"], "forwarded keys")
    T.eq(#rs.http_rules, 1, "http rules compiled")
end)
