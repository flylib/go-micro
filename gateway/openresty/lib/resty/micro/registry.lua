-- Reads go-micro service registrations from etcd, Consul and Nacos in the
-- exact format go-micro writes them (SPEC 4.2), normalised to
-- registry.Service: {name, version, metadata, endpoints, nodes[{id,
-- address, host, port, metadata}]}, one entry per version.

local http = require("resty.http")
local cjson = require("cjson.safe")

local ngx = ngx
local sbyte, schar, ssub = string.byte, string.char, string.sub
local concat = table.concat

local _M = {}

local TIMEOUT_MS = 3000

-- request tries each address until one answers.
function _M.request(addrs, path, opts)
    local last
    for _, addr in ipairs(addrs) do
        local c = http.new()
        c:set_timeouts(1000, TIMEOUT_MS, TIMEOUT_MS)
        local res, err = c:request_uri("http://" .. addr .. path, opts or {})
        if res then
            return res
        end
        last = addr .. ": " .. tostring(err)
    end
    return nil, last or "no registry address"
end

-- split_hostport returns host and port of "h:p" or "[v6]:p".
function _M.split_hostport(a)
    local h, p = a:match("^%[(.+)%]:(%d+)$")
    if not h then
        h, p = a:match("^([^:]+):(%d+)$")
    end
    if not h then
        return nil
    end
    return h, tonumber(p)
end

local function node(id, address, metadata)
    local host, port = _M.split_hostport(address)
    if not host then
        return nil
    end
    return { id = id, address = address, host = host, port = port, metadata = metadata or {} }
end

-- group collects single nodes into one Service per version, in first-seen
-- order.
local function group(name, entries)
    local by, order = {}, {}
    for _, e in ipairs(entries) do
        local s = by[e.version]
        if not s then
            s = { name = name, version = e.version, metadata = e.metadata or {}, endpoints = e.endpoints or {}, nodes = {} }
            by[e.version] = s
            order[#order + 1] = e.version
        end
        for _, n in ipairs(e.nodes) do
            s.nodes[#s.nodes + 1] = n
        end
    end
    local out = {}
    for _, v in ipairs(order) do
        out[#out + 1] = by[v]
    end
    return out
end

-- etcd (registry/etcd): one key per node, /micro/registry/<svc>/<id>,
-- holding a JSON registry.Service with that single node. Read through
-- the v3 JSON gateway.

local PREFIX = "/micro/registry/"

local function prefix_end(p)
    return ssub(p, 1, -2) .. schar(sbyte(p, -1) + 1)
end

function _M.parse_etcd(name, body)
    local doc = cjson.decode(body or "")
    if type(doc) ~= "table" then
        return nil, "etcd: bad response"
    end
    local entries = {}
    for _, kv in ipairs(doc.kvs or {}) do
        local svc = cjson.decode(ngx.decode_base64(kv.value or "") or "")
        if type(svc) == "table" then
            local nodes = {}
            for _, n in ipairs(svc.nodes or {}) do
                local nd = node(n.id, n.address or "", n.metadata)
                if nd then
                    nodes[#nodes + 1] = nd
                end
            end
            entries[#entries + 1] = {
                version = svc.version or "", metadata = svc.metadata,
                endpoints = svc.endpoints, nodes = nodes,
            }
        end
    end
    return group(name, entries)
end

function _M.fetch_etcd(cfg, name)
    local key = PREFIX .. name:gsub("/", "-") .. "/"
    local res, err = _M.request(cfg.addrs, "/v3/kv/range", {
        method = "POST",
        body = cjson.encode({ key = ngx.encode_base64(key), range_end = ngx.encode_base64(prefix_end(key)) }),
        headers = { ["Content-Type"] = "application/json" },
    })
    if not res then
        return nil, err
    end
    if res.status ~= 200 then
        return nil, "etcd: status " .. res.status
    end
    return _M.parse_etcd(name, res.body)
end

-- Consul (registry/consul): one Consul service per node, plain node
-- metadata in Meta, version in Meta.micro_version (F1).

function _M.parse_consul(name, body)
    local doc = cjson.decode(body or "")
    if type(doc) ~= "table" then
        return nil, "consul: bad response"
    end
    local entries = {}
    for _, e in ipairs(doc) do
        local s = e.Service or {}
        local host = (s.Address and s.Address ~= "") and s.Address or (e.Node or {}).Address
        local addr = (host and host:find(":", 1, true)) and ("[" .. host .. "]:" .. s.Port) or ((host or "") .. ":" .. tostring(s.Port))
        local meta = s.Meta or {}
        local nd = node(s.ID, addr, meta)
        if nd then
            entries[#entries + 1] = { version = meta.micro_version or "", nodes = { nd } }
        end
    end
    return group(name, entries)
end

function _M.fetch_consul(cfg, name)
    local res, err = _M.request(cfg.addrs, "/v1/health/service/" .. ngx.escape_uri(name) .. "?passing=true")
    if not res then
        return nil, err
    end
    if res.status ~= 200 then
        return nil, "consul: status " .. res.status
    end
    return _M.parse_consul(name, res.body)
end

-- Nacos (registry/nacos): one instance per node, plain metadata
-- including micro.version, read through the v1 open API.

function _M.parse_nacos(name, body)
    local doc = cjson.decode(body or "")
    if type(doc) ~= "table" then
        return nil, "nacos: bad response"
    end
    local entries = {}
    for _, h in ipairs(doc.hosts or {}) do
        if h.healthy ~= false and h.enabled ~= false then
            local meta = h.metadata or {}
            local addr = (tostring(h.ip):find(":", 1, true)) and ("[" .. h.ip .. "]:" .. h.port) or (h.ip .. ":" .. tostring(h.port))
            local nd = node(h.instanceId, addr, meta)
            if nd then
                entries[#entries + 1] = { version = meta["micro.version"] or "", nodes = { nd } }
            end
        end
    end
    return group(name, entries)
end

function _M.fetch_nacos(cfg, name)
    local q = {
        "serviceName=" .. ngx.escape_uri(name),
        "groupName=" .. ngx.escape_uri(cfg.group or "DEFAULT_GROUP"),
        "healthyOnly=true",
    }
    if cfg.namespace and cfg.namespace ~= "" then
        q[#q + 1] = "namespaceId=" .. ngx.escape_uri(cfg.namespace)
    end
    local res, err = _M.request(cfg.addrs, "/nacos/v1/ns/instance/list?" .. concat(q, "&"))
    if not res then
        return nil, err
    end
    -- nacos answers an unknown service with an error status
    if res.status >= 400 and res.status < 500 then
        return {}
    end
    if res.status ~= 200 then
        return nil, "nacos: status " .. res.status
    end
    return _M.parse_nacos(name, res.body)
end

local fetchers = { etcd = _M.fetch_etcd, consul = _M.fetch_consul, nacos = _M.fetch_nacos }

-- fetch returns the services registered under name ({} when none), or
-- nil and an error when the registry could not be read.
function _M.fetch(cfg, name)
    local f = fetchers[cfg.kind]
    if not f then
        return nil, "unsupported registry " .. tostring(cfg.kind)
    end
    return f(cfg, name)
end

return _M
