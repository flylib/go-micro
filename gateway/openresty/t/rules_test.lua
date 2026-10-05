-- Mirrors gateway/proxy/rules_test.go: both gateways must accept, reject
-- and route the same documents.

local rules = require("resty.micro.rules")

local function must(doc, registry)
    local rs, err = rules.parse(doc, registry or "nacos", 1)
    T.ok(rs, "parse: " .. tostring(err))
    return rs
end

local function derived(path)
    return (rules.split(path))
end

T.test("example rules parse", function()
    local f = assert(io.open("/t/t/rules.example.yaml"))
    local doc = f:read("*a")
    f:close()
    -- the example's public key is a placeholder: only plugin building
    -- may fail, schema and structure must pass
    local rs, err = rules.parse(doc, "nacos", 1)
    T.ok(not rs and tostring(err):find("jwt-auth", 1, true), "want only the placeholder key rejected, got " .. tostring(err))
end)

T.test("match precedence", function()
    local rs = must([[
version: 1
routes:
  - {name: by-service, match: {service: y}}
  - {name: short, match: {prefix: /y.}}
  - {name: long, match: {prefix: /y.Alt/}}
  - {name: exact, match: {method: /y.Svc/Echo}}
]])
    local cases = {
        ["/y.Svc/Echo"] = "exact",
        ["/y.Alt/Call"] = "long",
        ["/y.Svc/Call"] = "short",
        ["/z.Svc/Call"] = "", -- convention route
    }
    for path, want in pairs(cases) do
        local rt = rules.match(rs, path, derived(path))
        T.eq(rt and rt.name, want, path)
    end
end)

T.test("service route with upstream override", function()
    local rs = must("version: 1\nroutes: [{name: s, match: {service: y}, upstream: {service: real}}]\n")
    local rt = rules.match(rs, "/y.Svc/Call", "y")
    T.eq(rt.name, "s", "route")
    T.eq(rt.service, "real", "upstream service")
end)

T.test("convention off", function()
    local rs = must("version: 1\ndefaults: {convention: false}\n")
    T.eq(rules.match(rs, "/x.Svc/Call", "x"), nil, "unmatched call routed")
end)

T.test("defaults resolve", function()
    local rs = must([[
version: 1
defaults: {timeout: {connect: 2s}, retries: 4, selector: {strategy: random}}
routes:
  - {name: a, match: {service: a}, upstream: {timeout: {read: 30s}}}
  - {name: b, match: {service: b}, upstream: {retries: 0, selector: {version_weights: {v1: 1}}}}
]])
    local a = rs.services.a
    T.eq(a.connect, 2, "a.connect")
    T.eq(a.read, 30, "a.read")
    T.eq(a.send, rules.DEFAULT_SEND, "a.send")
    T.eq(a.retries, 4, "a.retries")
    T.eq(a.selector.strategy, "random", "a.strategy")
    local b = rs.services.b
    T.eq(b.retries, 0, "b.retries")
    T.eq(b.selector.strategy, "roundrobin", "b.strategy")
    T.eq(b.selector.version_weights.v1, 1, "b.weights")
    T.eq(rs.convention.connect, 2, "convention.connect")
    T.eq(rs.convention.retries, 4, "convention.retries")
end)

T.test("durations", function()
    local rs = must("version: 1\ndefaults: {timeout: {connect: 500ms, send: 1.5m, read: 1h}}\n")
    T.eq(rs.convention.connect, 0.5, "ms")
    T.eq(rs.convention.send, 90, "fractional minutes")
    T.eq(rs.convention.read, 3600, "hours")
end)

T.test("json documents", function()
    local rs = must('{"version": 1, "routes": [{"name": "j", "match": {"service": "s"}}]}')
    T.eq(rs.services.s.name, "j", "json route")
end)

T.test("rejected rules", function()
    local cases = {
        ["unknown plugin"] = 'version: 1\nglobal: {plugins: [{name: ip-restrction, config: {deny: ["1.2.3.4"]}}]}',
        ["x- plugin"] = "version: 1\nglobal: {plugins: [{name: x-lua, config: {}}]}",
        ["duplicate name"] = "version: 1\nroutes: [{name: a, match: {service: a}}, {name: a, match: {service: b}}]",
        ["duplicate method"] = "version: 1\nroutes: [{name: a, match: {method: /a.B/C}}, {name: b, match: {method: /a.B/C}}]",
        ["endpoint filter"] = "version: 1\nroutes: [{name: a, match: {service: a}, upstream: {filters: {endpoint: true}}}]",
        ["bad version"] = "version: 2",
        ["two matches"] = "version: 1\nroutes: [{name: a, match: {service: x, prefix: /x}}]",
        ["bad duration"] = "version: 1\ndefaults: {timeout: {connect: 1sec}}",
        ["jwt without key"] = "version: 1\nroutes: [{name: a, match: {service: x}, plugins: [{name: jwt-auth, config: {}}]}]",
        ["bad rate key"] = "version: 1\nglobal: {plugins: [{name: rate-limit, config: {rate: 1, key: user}}]}",
        ["zero rate"] = "version: 1\nglobal: {plugins: [{name: rate-limit, config: {rate: 0, key: client_ip}}]}",
    }
    for name, doc in pairs(cases) do
        T.eq(rules.parse(doc, "nacos", 1), nil, name .. " accepted")
    end
    -- the endpoint filter is fine where endpoint lists exist
    T.ok(rules.parse(cases["endpoint filter"], "etcd", 1), "endpoint filter with etcd rejected")
end)

T.test("flow-style numbers are numbers", function()
    local rs = must("version: 1\nroutes: [{name: l, match: {service: s}, upstream: {selector: {version_weights: {v1: 100, v2: 0}}}}]\n")
    T.eq(rs.services.s.selector.version_weights.v2, 0, "v2 weight")
end)

T.test("split derives the service", function()
    local cases = {
        ["/greeter.Greeter/Hello"] = { "greeter", "Greeter.Hello" },
        ["/go.micro.srv.greeter.Greeter/Hello"] = { "go.micro.srv.greeter", "Greeter.Hello" },
        ["/Greeter/Hello"] = { "", "Greeter.Hello" },
    }
    for path, want in pairs(cases) do
        local svc, ep = rules.split(path)
        T.eq(svc, want[1], path .. " service")
        T.eq(ep, want[2], path .. " endpoint")
    end
    for _, bad in ipairs({ "/malformed", "//x", "/a/b/c", "/a/" }) do
        T.eq(rules.split(bad), nil, bad .. " accepted")
    end
end)
