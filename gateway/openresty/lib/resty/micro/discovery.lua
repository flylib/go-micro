-- Service discovery cache (SPEC 4.4). Node lists live in a shared dict
-- so every worker sees the same view; one worker refreshes every
-- service that was asked for recently. A service seen for the first time
-- is fetched inline. When the registry cannot be read the last known
-- list is kept, so calls keep flowing through a registry outage.

local registry = require("resty.micro.registry")
local cjson = require("cjson.safe")
local lock = require("resty.lock")

local ngx = ngx

local _M = {}

local NODES = "micro_registry"
local WANT = "micro_want"
local WANT_TTL = 600 -- stop refreshing a service nobody called for 10 min

local cfg
local decoded = {} -- per worker: name -> {ver = n, services = {...}}

function _M.init(registry_cfg)
    cfg = registry_cfg
end

-- store publishes a fetched list; the version only moves on change so
-- workers re-decode only then.
local function store(name, services)
    local dict = ngx.shared[NODES]
    local js = cjson.encode(services)
    if dict:get("s:" .. name) == js then
        return
    end
    dict:set("s:" .. name, js)
    dict:incr("v:" .. name, 1, 0)
end

local function refresh(name)
    local services, err = registry.fetch(cfg, name)
    if not services then
        return nil, err
    end
    store(name, services)
    return true
end

-- get returns the cached services of name, fetching on first use.
function _M.get(name)
    local dict = ngx.shared[NODES]
    ngx.shared[WANT]:set(name, true, WANT_TTL)

    local ver = dict:get("v:" .. name)
    if not ver then
        -- first call for this service: fetch once, under a lock so a
        -- burst of first calls makes one registry request
        local l = lock:new("micro_locks", { timeout = 5 })
        local ok = l and l:lock("fetch:" .. name)
        if not dict:get("v:" .. name) then
            local _, err = refresh(name)
            if err then
                ngx.log(ngx.WARN, "gateway: registry lookup ", name, ": ", err)
            end
        end
        if ok then
            l:unlock()
        end
        ver = dict:get("v:" .. name)
        if not ver then
            return {}
        end
    end

    local d = decoded[name]
    if d and d.ver == ver then
        return d.services
    end
    local services = cjson.decode(dict:get("s:" .. name) or "[]") or {}
    decoded[name] = { ver = ver, services = services }
    return services
end

-- poll refreshes every recently requested service. Run by one worker.
function _M.poll()
    local names = ngx.shared[WANT]:get_keys(0)
    for _, name in ipairs(names) do
        local _, err = refresh(name)
        if err then
            ngx.log(ngx.WARN, "gateway: registry refresh ", name, ", keeping last known nodes: ", err)
        end
    end
end

return _M
