-- Rules sources (SPEC 11): the same URIs as the Go gateway, read by
-- polling. file:// is read from disk; etcd://, consul:// and nacos://
-- are read over HTTP.

local registry = require("resty.micro.registry")
local cjson = require("cjson.safe")

local ngx = ngx
local concat = table.concat

local _M = {}

-- parse turns a MICRO_GATEWAY_RULES URI into a source description.
function _M.parse(uri)
    local scheme, rest = uri:match("^(%a+)://(.*)$")
    if not scheme then
        return nil, "rules source: not a URI: " .. uri
    end
    local query = ""
    local q = rest:find("?", 1, true)
    if q then
        rest, query = rest:sub(1, q - 1), rest:sub(q + 1)
    end
    local params = ngx.decode_args(query) or {}
    if scheme == "file" then
        return { kind = "file", path = rest }
    end
    local host, path = rest:match("^([^/]+)/(.+)$")
    if not host then
        return nil, "rules source: " .. scheme .. " URI needs host and key: " .. uri
    end
    if scheme == "etcd" then
        return { kind = "etcd", addrs = { host }, key = "/" .. path }
    elseif scheme == "consul" then
        return { kind = "consul", addrs = { host }, key = path }
    elseif scheme == "nacos" then
        return {
            kind = "nacos", addrs = { host }, data_id = path,
            group = params.group or "DEFAULT_GROUP", namespace = params.namespace,
        }
    end
    return nil, "rules source: unsupported scheme " .. scheme .. " (want file, etcd, consul or nacos)"
end

function _M.read_file(src)
    local f, err = io.open(src.path, "rb")
    if not f then
        return nil, err
    end
    local s = f:read("*a")
    f:close()
    return s
end

local function read_etcd(src)
    local res, err = registry.request(src.addrs, "/v3/kv/range", {
        method = "POST",
        body = cjson.encode({ key = ngx.encode_base64(src.key) }),
        headers = { ["Content-Type"] = "application/json" },
    })
    if not res then
        return nil, err
    end
    local doc = cjson.decode(res.body or "")
    local kv = type(doc) == "table" and doc.kvs and doc.kvs[1]
    if res.status ~= 200 or not kv then
        return nil, "etcd: key " .. src.key .. " not found"
    end
    return ngx.decode_base64(kv.value or "")
end

local function read_consul(src)
    local res, err = registry.request(src.addrs, "/v1/kv/" .. src.key .. "?raw")
    if not res then
        return nil, err
    end
    if res.status ~= 200 then
        return nil, "consul: key " .. src.key .. ": status " .. res.status
    end
    return res.body
end

local function read_nacos(src)
    local q = { "dataId=" .. ngx.escape_uri(src.data_id), "group=" .. ngx.escape_uri(src.group) }
    if src.namespace and src.namespace ~= "" then
        q[#q + 1] = "tenant=" .. ngx.escape_uri(src.namespace)
    end
    local res, err = registry.request(src.addrs, "/nacos/v1/cs/configs?" .. concat(q, "&"))
    if not res then
        return nil, err
    end
    if res.status ~= 200 then
        return nil, "nacos: config " .. src.data_id .. ": status " .. res.status
    end
    return res.body
end

local readers = { file = _M.read_file, etcd = read_etcd, consul = read_consul, nacos = read_nacos }

-- read returns the current document of src.
function _M.read(src)
    return readers[src.kind](src)
end

return _M
