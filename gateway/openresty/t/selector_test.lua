-- Mirrors gateway/proxy/balancer_test.go, plus the registry parsers.

local selector = require("resty.micro.selector")
local registry = require("resty.micro.registry")

local function node(id, proto)
    return { id = id, address = id .. ":1", host = id, port = 1, metadata = { protocol = proto } }
end

T.test("eligible skips mucp", function()
    local svcs = { { name = "s", version = "v1", nodes = { node("a", "grpc"), node("b", "mucp"), node("c", nil) } } }
    local out = selector.eligible(svcs, {}, "")
    T.eq(selector.count(out), 1, "eligible nodes")
    T.eq(out[1].nodes[1].id, "a", "eligible node")
    T.eq(#svcs[1].nodes, 3, "registry services mutated")
end)

T.test("eligible filters", function()
    local a, b = node("a", "grpc"), node("b", "grpc")
    b.metadata.zone = "z1"
    local svcs = {
        { name = "s", version = "v1", nodes = { a } },
        { name = "s", version = "v2", nodes = { b }, endpoints = { { name = "Svc.Call" } } },
    }
    local out = selector.eligible(svcs, { version = "v2" }, "")
    T.eq(selector.count(out) == 1 and out[1].version, "v2", "version filter")
    out = selector.eligible(svcs, { labels = { zone = "z1" } }, "")
    T.eq(selector.count(out) == 1 and out[1].nodes[1].id, "b", "label filter")
    out = selector.eligible(svcs, { endpoint = true }, "Svc.Call")
    T.eq(selector.count(out) == 1 and out[1].version, "v2", "endpoint filter")
end)

T.test("roundrobin rotates across calls", function()
    local rt = { selector = { strategy = "roundrobin" } }
    local svcs = { { nodes = { node("a", "grpc"), node("b", "grpc"), node("c", "grpc") } } }
    local seen = {}
    for _ = 1, 30 do
        local first = selector.candidates(rt, svcs)[1].id
        seen[first] = (seen[first] or 0) + 1
    end
    for _, id in ipairs({ "a", "b", "c" }) do
        T.eq(seen[id], 10, "picks of " .. id)
    end
end)

T.test("candidates never repeat", function()
    for _, strat in ipairs({ "roundrobin", "random", "weighted", "p2c" }) do
        local rt = { selector = { strategy = strat } }
        local svcs = { { nodes = { node("a", "grpc"), node("b", "grpc") } } }
        local c = selector.candidates(rt, svcs)
        T.eq(#c, 2, strat .. " candidates")
        T.ok(c[1].address ~= c[2].address, strat .. " repeated a node")
    end
end)

T.test("version weights exclude zero", function()
    local rt = { selector = { strategy = "roundrobin", version_weights = { v1 = 100, v2 = 0 } } }
    local svcs = {
        { version = "v1", nodes = { node("a", "grpc") } },
        { version = "v2", nodes = { node("b", "grpc") } },
    }
    for _ = 1, 50 do
        local c = selector.candidates(rt, svcs)
        T.eq(#c, 1, "candidates")
        T.eq(c[1].id, "a", "picked")
    end
end)

T.test("p2c prefers the lighter node", function()
    local rt = { selector = { strategy = "p2c" } }
    local svcs = { { nodes = { node("slow", "grpc"), node("fast", "grpc") } } }
    selector.p2c_begin("slow:1")
    selector.p2c_end("slow:1", 2.0)
    selector.p2c_begin("fast:1")
    selector.p2c_end("fast:1", 0.001)
    for _ = 1, 20 do
        T.eq(selector.candidates(rt, svcs)[1].id, "fast", "p2c pick")
    end
end)

-- Registry responses, shaped as each backend returns go-micro's data.

T.test("parse etcd", function()
    local svc = '{"name":"s","version":"v2","metadata":{},"endpoints":[{"name":"Svc.Call"}],'
        .. '"nodes":[{"id":"s-1","address":"10.0.0.1:50051","metadata":{"protocol":"grpc"}}]}'
    local body = '{"kvs":[{"key":"x","value":"' .. ngx.encode_base64(svc) .. '"}]}'
    local out = assert(registry.parse_etcd("s", body))
    T.eq(#out, 1, "services")
    T.eq(out[1].version, "v2", "version")
    T.eq(out[1].endpoints[1].name, "Svc.Call", "endpoints")
    T.eq(out[1].nodes[1].host, "10.0.0.1", "host")
    T.eq(out[1].nodes[1].port, 50051, "port")
    T.eq(#assert(registry.parse_etcd("s", "{}")), 0, "no keys")
end)

T.test("parse consul", function()
    local body = [==[[
      {"Node":{"Address":"10.9.9.9"},"Service":{"ID":"s-1","Service":"s","Address":"10.0.0.1","Port":50051,
        "Meta":{"protocol":"grpc","micro_version":"v1"}}},
      {"Node":{"Address":"10.0.0.2"},"Service":{"ID":"s-2","Service":"s","Address":"","Port":50052,
        "Meta":{"protocol":"grpc","micro_version":"v2"}}}
    ]]==]
    local out = assert(registry.parse_consul("s", body))
    T.eq(#out, 2, "one service per version")
    T.eq(out[1].nodes[1].address, "10.0.0.1:50051", "service address")
    T.eq(out[2].nodes[1].address, "10.0.0.2:50052", "falls back to node address")
    T.eq(out[2].version, "v2", "version from Meta")
end)

T.test("parse nacos", function()
    local body = [[{"hosts":[
      {"instanceId":"i1","ip":"10.0.0.1","port":50051,"healthy":true,"enabled":true,"metadata":{"protocol":"grpc","micro.version":"v1"}},
      {"instanceId":"i2","ip":"10.0.0.2","port":50051,"healthy":false,"enabled":true,"metadata":{"protocol":"grpc","micro.version":"v1"}}
    ]}]]
    local out = assert(registry.parse_nacos("s", body))
    T.eq(selector.count(out), 1, "unhealthy instance kept")
    T.eq(out[1].nodes[1].id, "i1", "id")
    T.eq(out[1].version, "v1", "version")
end)

T.test("ip prefixes", function()
    local ip = require("resty.micro.ip")
    -- /15 spans 10.2.0.0-10.3.255.255: the partial-byte path
    local p = assert(ip.prefix("10.2.0.0/15"))
    T.ok(not ip.contains(p, ip.parse("10.1.255.1")), "10.1.255.1 in 10.2.0.0/15")
    T.ok(ip.contains(p, ip.parse("10.3.200.1")), "10.3.200.1 not in 10.2.0.0/15")
    T.ok(not ip.contains(p, ip.parse("10.4.0.1")), "10.4.0.1 in 10.2.0.0/15")
    T.ok(ip.contains(assert(ip.prefix("::/0")), ip.parse("2001:db8::1")), "::/0")
    T.ok(not ip.contains(assert(ip.prefix("::/0")), ip.parse("1.2.3.4")), "v4 in ::/0")
    T.eq(ip.prefix("10.0.0.0/33"), nil, "bad length accepted")
end)
