-- Node eligibility and ordering (SPEC 4.3, 5). Strategy and filter
-- semantics follow go-micro's selector packages: FilterVersion,
-- FilterLabel, FilterEndpoint, RoundRobin, Random, weighted.Strategy,
-- weighted.VersionWeights and p2c.Strategy.
--
-- A call gets an ordered candidate list up front: the first entry is the
-- strategy's pick, the rest are the retries (SPEC 6), never repeating an
-- address.

local random = math.random
local floor, exp = math.floor, math.exp
local sort = table.sort

local _M = {}

local DEFAULT_WEIGHT = 100     -- selector/weighted.defaultWeight
local P2C_TAU = 0.6            -- selector/p2c.tau, seconds
local P2C_PENALTY = 10         -- selector/p2c.penalty, seconds
local P2C_DICT = "micro_p2c"

-- eligible keeps gRPC nodes (mucp speaks a protocol the gateway does
-- not) and applies the route's filters. Services are not modified.
function _M.eligible(services, filters, endpoint)
    local out = {}
    filters = filters or {}
    for _, s in ipairs(services or {}) do
        local keep = filters.version == nil or s.version == filters.version
        if keep and filters.endpoint == true then
            keep = false
            for _, ep in ipairs(s.endpoints or {}) do
                if ep.name == endpoint then
                    keep = true
                    break
                end
            end
        end
        if keep then
            local nodes = {}
            for _, n in ipairs(s.nodes or {}) do
                local md = n.metadata or {}
                local ok = md.protocol == "grpc"
                if ok and filters.labels then
                    for k, v in pairs(filters.labels) do
                        if md[k] ~= v then
                            ok = false
                            break
                        end
                    end
                end
                if ok then
                    nodes[#nodes + 1] = n
                end
            end
            if #nodes > 0 then
                out[#out + 1] = { name = s.name, version = s.version, nodes = nodes }
            end
        end
    end
    return out
end

local function flatten(services)
    local nodes = {}
    for _, s in ipairs(services) do
        for _, n in ipairs(s.nodes) do
            nodes[#nodes + 1] = n
        end
    end
    return nodes
end

-- weighted_order draws entries without replacement, each draw weighted;
-- zero-weight entries never come up.
local function weighted_order(entries)
    local out = {}
    local pool = {}
    for _, e in ipairs(entries) do
        if e.weight > 0 then
            pool[#pool + 1] = e
        end
    end
    while #pool > 0 do
        local total = 0
        for _, e in ipairs(pool) do
            total = total + e.weight
        end
        local r = random() * total
        local pick = #pool
        for i, e in ipairs(pool) do
            r = r - e.weight
            if r < 0 then
                pick = i
                break
            end
        end
        out[#out + 1] = pool[pick].node
        table.remove(pool, pick)
    end
    return out
end

local function round_robin(rt, services)
    local nodes = flatten(services)
    sort(nodes, function(a, b) return a.id < b.id end)
    -- the route keeps the position, so calls rotate (per worker)
    rt.rr = (rt.rr or 0) + 1
    local out = {}
    for i = 0, #nodes - 1 do
        out[#out + 1] = nodes[(rt.rr + i) % #nodes + 1]
    end
    return out
end

local function shuffled(services)
    local nodes = flatten(services)
    for i = #nodes, 2, -1 do
        local j = random(i)
        nodes[i], nodes[j] = nodes[j], nodes[i]
    end
    return nodes
end

local function by_metadata_weight(services)
    local entries = {}
    for _, n in ipairs(flatten(services)) do
        local w = tonumber((n.metadata or {}).weight)
        if not w or w < 0 or w ~= floor(w) then
            w = DEFAULT_WEIGHT
        end
        entries[#entries + 1] = { node = n, weight = w }
    end
    return weighted_order(entries)
end

-- by_version_weights: unlisted versions get no traffic, nodes of a
-- version share its weight (at least 1 each).
local function by_version_weights(weights, services)
    local entries = {}
    for _, s in ipairs(services) do
        local w = weights[s.version]
        if w and w > 0 and #s.nodes > 0 then
            local per = floor(w / #s.nodes)
            if per < 1 then
                per = 1
            end
            for _, n in ipairs(s.nodes) do
                entries[#entries + 1] = { node = n, weight = per }
            end
        end
    end
    return weighted_order(entries)
end

-- p2c load = EWMA latency x (inflight + 1), latency decayed with age.
local function p2c_load(dict, addr, now)
    local lag = dict:get("lag:" .. addr)
    if not lag or lag == 0 then
        lag = P2C_PENALTY
    else
        local elapsed = now - (dict:get("stamp:" .. addr) or now)
        if elapsed > 0 then
            lag = lag * exp(-elapsed / P2C_TAU)
            if lag <= 0 then
                lag = 1e-9
            end
        end
    end
    return lag * ((dict:get("inflight:" .. addr) or 0) + 1)
end

local function p2c(services)
    local nodes = shuffled(services)
    if #nodes < 2 then
        return nodes
    end
    local dict = ngx.shared[P2C_DICT]
    local now = ngx.now()
    -- the first two of a shuffle are two random distinct nodes
    if p2c_load(dict, nodes[2].address, now) < p2c_load(dict, nodes[1].address, now) then
        nodes[1], nodes[2] = nodes[2], nodes[1]
    end
    return nodes
end

-- p2c_begin and p2c_end feed one call to addr back into the strategy,
-- as selector/p2c.Track does.
function _M.p2c_begin(addr)
    ngx.shared[P2C_DICT]:incr("inflight:" .. addr, 1, 0)
end

function _M.p2c_end(addr, rt_seconds)
    local dict = ngx.shared[P2C_DICT]
    dict:incr("inflight:" .. addr, -1, 0)
    if not rt_seconds then
        return
    end
    local now = ngx.now()
    local old = dict:get("lag:" .. addr)
    if not old or old == 0 then
        dict:set("lag:" .. addr, rt_seconds)
    else
        local w = exp(-(now - (dict:get("stamp:" .. addr) or now)) / P2C_TAU)
        dict:set("lag:" .. addr, old * w + rt_seconds * (1 - w))
    end
    dict:set("stamp:" .. addr, now)
end

-- candidates orders the eligible nodes for one call.
function _M.candidates(rt, services)
    local nodes
    local sel = rt.selector or {}
    if sel.version_weights then
        nodes = by_version_weights(sel.version_weights, services)
    elseif sel.strategy == "random" then
        nodes = shuffled(services)
    elseif sel.strategy == "weighted" then
        nodes = by_metadata_weight(services)
    elseif sel.strategy == "p2c" then
        nodes = p2c(services)
    else
        nodes = round_robin(rt, services)
    end
    local seen, out = {}, {}
    for _, n in ipairs(nodes) do
        if not seen[n.address] then
            seen[n.address] = true
            out[#out + 1] = n
        end
    end
    return out
end

function _M.count(services)
    local n = 0
    for _, s in ipairs(services) do
        n = n + #s.nodes
    end
    return n
end

return _M
