-- Mirrors gateway/proxy/plugins_test.go.

local plugins = require("resty.micro.plugins")
local errors = require("resty.micro.errors")
local headers = require("resty.micro.headers")
local pkey = require("resty.openssl.pkey")
local hmac = require("resty.openssl.hmac")
local b64 = require("ngx.base64")
local cjson = require("cjson.safe")

local function build(name, config)
    local ps, err = plugins.build({ { name = name, config = config } }, "t" .. math.random(1e9))
    T.ok(ps, "build " .. name .. ": " .. tostring(err))
    return ps[1]
end

local function code(err)
    return err and err.grpc or 0
end

T.test("ip-restriction", function()
    local p = build("ip-restriction", { allow = { "10.0.0.0/8", "2001:db8::/32" }, deny = { "10.1.0.0/16", "10.2.3.4" } })
    local cases = {
        ["10.9.9.9"] = 0,
        ["10.1.2.3"] = errors.PERMISSION_DENIED, -- deny beats allow
        ["10.2.3.4"] = errors.PERMISSION_DENIED, -- bare IP
        ["192.168.1.1"] = errors.PERMISSION_DENIED, -- not in allow
        ["2001:db8::1"] = 0,
        ["::ffff:10.9.9.9"] = 0, -- IPv4-mapped
        ["garbage"] = errors.PERMISSION_DENIED,
    }
    for addr, want in pairs(cases) do
        T.eq(code(p:check({ client_ip = addr, headers = {} })), want, addr)
    end
end)

local function seg(v)
    return b64.encode_base64url(cjson.encode(v))
end

local function signer()
    local key = assert(pkey.new({ type = "RSA", bits = 2048 }))
    local pub = ngx.encode_base64(assert(key:tostring("public", "PEM")))
    return {
        pub = pub,
        rs256 = function(claims)
            local input = seg({ alg = "RS256", typ = "JWT" }) .. "." .. seg(claims)
            local sig = assert(key:sign(input, "sha256"))
            return input .. "." .. b64.encode_base64url(sig)
        end,
    }
end

local function bearer(tok)
    return { authorization = "Bearer " .. tok }
end

T.test("jwt-auth", function()
    local s = signer()
    local p = build("jwt-auth", { public_key = s.pub, scopes = { "orders", "admin" } })
    local now = ngx.time()
    local exp = now + 60

    local call = { headers = bearer(s.rs256({ sub = "u1", exp = exp, scopes = { "orders" } })) }
    T.eq(code(p:check(call)), 0, "valid token")
    T.eq(call.account, "u1", "account")

    local pem = ngx.decode_base64(s.pub)
    local function hs(secret)
        local input = seg({ alg = "HS256" }) .. "." .. seg({ sub = "x", exp = exp })
        local mac = assert(hmac.new(secret, "sha256")):final(input)
        return input .. "." .. b64.encode_base64url(mac)
    end
    local cases = {
        ["no token"] = { {}, errors.UNAUTHENTICATED },
        ["not bearer"] = { { authorization = "Basic x" }, errors.UNAUTHENTICATED },
        ["garbage"] = { bearer("a.b.c"), errors.UNAUTHENTICATED },
        ["expired"] = { bearer(s.rs256({ sub = "u", exp = now - 60, scopes = { "orders" } })), errors.UNAUTHENTICATED },
        ["within leeway"] = { bearer(s.rs256({ sub = "u", exp = now - 10, scopes = { "orders" } })), 0 },
        ["no exp"] = { bearer(s.rs256({ sub = "u", scopes = { "orders" } })), errors.UNAUTHENTICATED },
        ["wrong scope"] = { bearer(s.rs256({ sub = "u", exp = exp, scopes = { "billing" } })), errors.PERMISSION_DENIED },
        ["hs256 pem secret"] = { bearer(hs(pem)), errors.UNAUTHENTICATED },
        ["hs256 b64 secret"] = { bearer(hs(s.pub)), errors.UNAUTHENTICATED },
        ["other key"] = { bearer(signer().rs256({ sub = "u", exp = exp, scopes = { "orders" } })), errors.UNAUTHENTICATED },
    }
    for name, c in pairs(cases) do
        T.eq(code(p:check({ headers = c[1] })), c[2], name)
    end
end)

T.test("rate-limit bucket", function()
    local p = build("rate-limit", { rate = 1, burst = 1, key = "client_ip" })
    local a, b = { client_ip = "1.1.1.1", headers = {} }, { client_ip = "2.2.2.2", headers = {} }
    -- capacity is 1+burst = 2
    T.eq(code(p:check(a)), 0, "call 1")
    T.eq(code(p:check(a)), 0, "call 2")
    T.eq(code(p:check(a)), errors.RESOURCE_EXHAUSTED, "call 3")
    T.eq(code(p:check(b)), 0, "keys share a bucket")
end)

T.test("rate-limit keys", function()
    local p = build("rate-limit", { rate = 1, key = "account" })
    T.eq(p:keyof({ client_ip = "1.1.1.1", headers = {} }), "ip:1.1.1.1", "unauthenticated")
    T.eq(p:keyof({ client_ip = "1.1.1.1", account = "u", headers = {} }), "account:u", "account")
    p = build("rate-limit", { rate = 1, key = "header:X-Tenant" })
    T.eq(p:keyof({ client_ip = "1.1.1.1", headers = { ["x-tenant"] = "t1" } }), "header:t1", "header")
end)

T.test("traceparent", function()
    T.ok(headers.valid_traceparent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"), "valid rejected")
    T.ok(not headers.valid_traceparent("00-00000000000000000000000000000000-0000000000000000-01"), "zero ids accepted")
    T.ok(not headers.valid_traceparent("00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01"), "uppercase accepted")
    T.ok(not headers.valid_traceparent("01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"), "version 01 accepted")
    T.ok(headers.valid_traceparent(headers.new_traceparent()), "generated traceparent invalid")
end)

T.test("client ip", function()
    local ip = require("resty.micro.ip")
    local trusted = assert(ip.prefixes({ "10.0.0.0/8" }))
    local xff = "6.6.6.6, 7.7.7.7, 10.0.0.9"
    T.eq(headers.client_ip("10.0.0.5", xff, trusted), "7.7.7.7", "behind trusted proxies")
    T.eq(headers.client_ip("8.8.8.8", xff, trusted), "8.8.8.8", "untrusted peer believed")
    T.eq(headers.client_ip("10.0.0.5", xff, {}), "10.0.0.5", "no trusted proxies")
end)

T.test("error encoding", function()
    T.eq(errors.json(503, "no available nodes for x"),
        '{"id":"micro.gateway","code":503,"detail":"no available nodes for x","status":"Service Unavailable"}', "json")
    T.eq(errors.percent_encode('a%b "c"\n'), 'a%25b "c"%0A', "grpc-message encoding")
end)
