-- Rules documents (SPEC 9): parse YAML or JSON, validate against
-- rules.schema.json, compile into a matcher. Mirrors gateway/proxy's
-- rules.go so both gateways accept, reject and route the same way.

local cjson = require("cjson.safe")
local yaml = require("tinyyaml")
local jsonschema = require("jsonschema")
local plugins = require("resty.micro.plugins")
local httprules = require("resty.micro.httprules")

local sfind, ssub, sort = string.find, string.sub, table.sort

local _M = {}

-- Built-in defaults (SPEC 6), in seconds.
_M.DEFAULT_CONNECT = 1
_M.DEFAULT_SEND = 10
_M.DEFAULT_READ = 10
_M.DEFAULT_RETRIES = 2

local validator

-- strip_defaults removes "default" keywords: this validator writes them
-- into the document it checks (an APISIX feature), which would make
-- e.g. a route's partial timeout override the defaults section. Defaults
-- are applied when compiling, as in the Go gateway. Keys of maps of
-- schemas (properties, $defs) are names, not keywords, and are kept.
local NAMED = { properties = true, ["$defs"] = true, patternProperties = true }

local function strip_defaults(schema, names)
    if type(schema) ~= "table" then
        return
    end
    for k, v in pairs(schema) do
        if names then
            strip_defaults(v, false)
        elseif k == "default" then
            schema[k] = nil
        else
            strip_defaults(v, NAMED[k] == true)
        end
    end
end

-- load_schema compiles the schema shipped next to this file. The
-- library does not resolve absolute $id URLs, and neither $id nor
-- $schema affects validation, so both are dropped.
function _M.load_schema(path)
    local f, err = io.open(path, "rb")
    if not f then
        return nil, err
    end
    local s = f:read("*a")
    f:close()
    local schema = cjson.decode(s)
    if type(schema) ~= "table" then
        return nil, "schema is not JSON"
    end
    schema["$id"] = nil
    schema["$schema"] = nil
    strip_defaults(schema, false)
    local ok, v = pcall(jsonschema.generate_validator, schema)
    if not ok then
        return nil, v
    end
    validator = v
    return true
end

-- plain turns the YAML parser's tables (with type metatables, yaml null
-- objects) into plain values as cjson would decode them.
local function plain(v)
    if type(v) ~= "table" then
        return v
    end
    local mt = getmetatable(v)
    if mt and tostring(v) == "yaml.null" then
        return cjson.null
    end
    local out = {}
    for k, x in pairs(v) do
        out[plain(k)] = plain(x)
    end
    return out
end

local function decode(text)
    -- JSON first: it is valid YAML, but cjson is exact about it
    local doc = cjson.decode(text)
    if doc ~= nil then
        return doc
    end
    local ok, parsed = pcall(yaml.parse, text)
    if not ok then
        return nil, "parse rules: " .. tostring(parsed)
    end
    return plain(parsed)
end

-- duration parses "500ms", "1s", "1.5m", "2h" into seconds.
local UNITS = { ms = 0.001, s = 1, m = 60, h = 3600 }
local function duration(s)
    local n, unit = tostring(s):match("^(%d+%.?%d*)(%a+)$")
    local mult = UNITS[unit or ""]
    if not n or not mult then
        return nil
    end
    return tonumber(n) * mult
end

local function apply_timeouts(rt, t)
    for _, key in ipairs({ "connect", "send", "read" }) do
        local v = t and t[key]
        if v ~= nil then
            local d = duration(v)
            if not d or d <= 0 then
                return nil, "timeout." .. key .. " must be a positive duration"
            end
            rt[key] = d
        end
    end
    return true
end

local function normalize_selector(s)
    local out = { strategy = s.strategy or "roundrobin", version_weights = s.version_weights }
    if out.version_weights and next(out.version_weights) == nil then
        out.version_weights = nil
    end
    return out
end

local function defaults_route(d)
    local rt = {
        name = "",
        selector = { strategy = "roundrobin" },
        connect = _M.DEFAULT_CONNECT,
        send = _M.DEFAULT_SEND,
        read = _M.DEFAULT_READ,
        retries = _M.DEFAULT_RETRIES,
        plugins = {},
        filters = {},
    }
    d = d or {}
    if type(d.selector) == "table" then
        rt.selector = normalize_selector(d.selector)
    end
    if d.retries ~= nil then
        rt.retries = d.retries
    end
    local ok, err = apply_timeouts(rt, d.timeout)
    if not ok then
        return nil, "defaults: " .. err
    end
    return rt
end

local function compile_route(spec, base, registry, uid)
    local up = spec.upstream or {}
    local rt = {
        name = spec.name,
        service = up.service,
        selector = base.selector,
        filters = up.filters or {},
        connect = base.connect,
        send = base.send,
        read = base.read,
        retries = base.retries,
        rr = 0,
    }
    if type(up.selector) == "table" then
        rt.selector = normalize_selector(up.selector)
    end
    if up.retries ~= nil then
        rt.retries = up.retries
    end
    local ok, err = apply_timeouts(rt, up.timeout)
    if not ok then
        return nil, err
    end
    -- only etcd stores endpoint lists; elsewhere the filter would pass
    -- every node, so refuse it instead (SPEC 5.2)
    if rt.filters.endpoint == true and registry ~= "etcd" then
        return nil, "filters.endpoint needs the etcd registry, have " .. tostring(registry)
    end
    rt.plugins, err = plugins.build(spec.plugins, uid)
    if not rt.plugins then
        return nil, err
    end
    return rt
end

-- compile builds a rule set from a validated document.
local function compile(doc, registry, generation)
    local base, err = defaults_route(doc.defaults)
    if not base then
        return nil, err
    end
    local rs = { methods = {}, prefixes = {}, services = {} }
    local d = doc.defaults or {}
    if d.convention == nil or d.convention == true then
        rs.convention = base
    end
    local g = doc.global or {}
    rs.global, err = plugins.build(g.plugins, generation .. ".global")
    if not rs.global then
        return nil, "global: " .. err
    end

    local names = {}
    for i, spec in ipairs(doc.routes or {}) do
        if names[spec.name] then
            return nil, "route " .. spec.name .. ": duplicate name"
        end
        names[spec.name] = true
        local rt
        rt, err = compile_route(spec, base, registry, generation .. ".r" .. i)
        if not rt then
            return nil, "route " .. spec.name .. ": " .. err
        end
        local m = spec.match
        if m.method then
            if rs.methods[m.method] then
                return nil, "route " .. spec.name .. ": method " .. m.method .. " already matched by another route"
            end
            rs.methods[m.method] = rt
        elseif m.prefix then
            for _, p in ipairs(rs.prefixes) do
                if p.prefix == m.prefix then
                    return nil, "route " .. spec.name .. ": prefix " .. m.prefix .. " already matched by another route"
                end
            end
            rs.prefixes[#rs.prefixes + 1] = { prefix = m.prefix, route = rt, order = i }
        else
            if rs.services[m.service] then
                return nil, "route " .. spec.name .. ": service " .. m.service .. " already matched by another route"
            end
            rs.services[m.service] = rt
        end
    end
    -- longest prefix first; ties keep document order, as Go's stable sort
    sort(rs.prefixes, function(a, b)
        if #a.prefix ~= #b.prefix then
            return #a.prefix > #b.prefix
        end
        return a.order < b.order
    end)
    rs.http_rules, err = httprules.compile(doc.http_rules)
    if not rs.http_rules then
        return nil, err
    end

    -- keys any jwt-auth forwards are stripped from every inbound call,
    -- whatever its route, so clients cannot set them (SPEC 9.5)
    rs.forwarded = {}
    local function collect(list)
        for _, p in ipairs(list) do
            for k in pairs(p.forward or {}) do
                rs.forwarded[k] = true
            end
        end
    end
    collect(rs.global)
    for _, rt in pairs(rs.methods) do collect(rt.plugins) end
    for _, rt in pairs(rs.services) do collect(rt.plugins) end
    for _, p in ipairs(rs.prefixes) do collect(p.route.plugins) end
    return rs
end

-- parse validates and compiles a rules document. registry gates
-- registry-dependent features; generation makes plugin state unique per
-- loaded document.
function _M.parse(text, registry, generation)
    if not validator then
        return nil, "rules schema not loaded"
    end
    local doc, err = decode(text)
    if doc == nil then
        return nil, err or "parse rules: empty document"
    end
    local ok, verr = validator(doc)
    if not ok then
        return nil, "rules violate schema: " .. tostring(verr)
    end
    return compile(doc, registry, tostring(generation or 0))
end

-- match picks the route for a call (SPEC 3.2): exact method, then
-- longest prefix, then the derived service, then convention routing.
function _M.match(rs, method, derived)
    local rt = rs.methods[method]
    if rt then
        return rt
    end
    for _, p in ipairs(rs.prefixes) do
        if ssub(method, 1, #p.prefix) == p.prefix then
            return p.route
        end
    end
    rt = rs.services[derived]
    if rt then
        return rt
    end
    return rs.convention
end

-- split checks a gRPC path (SPEC 3.1) and derives the service the way
-- util/grpc.ServiceFromMethod does: everything before the last '.' of
-- the middle part. It returns nil for a malformed path.
function _M.split(path)
    local middle, method = path:match("^/([^/]+)/([^/]+)$")
    if not middle then
        return nil
    end
    local dot
    local from = 1
    while true do
        local i = sfind(middle, ".", from, true)
        if not i then
            break
        end
        dot, from = i, i + 1
    end
    local service = dot and ssub(middle, 1, dot - 1) or ""
    local handler = dot and ssub(middle, dot + 1) or middle
    return service, handler .. "." .. method
end

return _M
