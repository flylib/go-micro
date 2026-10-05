-- The OpenResty implementation of the go-micro edge gateway
-- (gateway/SPEC.md). nginx.conf wires these entry points:
--
--   init_by_lua         gateway.init()         bootstrap settings, schema, file rules
--   init_worker_by_lua  gateway.init_worker()  rules and registry polling (worker 0)
--   access_by_lua       gateway.access()       route, plugins, node candidates, headers
--   balancer_by_lua     gateway.balance()      next candidate per try (SPEC 6)
--   log_by_lua          gateway.log()          p2c feedback
--   error_page 502/504  gateway.upstream_error(...)
--
-- grpc_pass relays frames, statuses and trailers untouched (SPEC 8), and
-- nginx never retries a POST once it reached a node, so a retry only
-- happens before the request was sent (SPEC 6).

local rules = require("resty.micro.rules")
local source = require("resty.micro.source")
local discovery = require("resty.micro.discovery")
local selector = require("resty.micro.selector")
local headers = require("resty.micro.headers")
local errors = require("resty.micro.errors")
local ip = require("resty.micro.ip")
local balancer = require("ngx.balancer")
local cjson = require("cjson.safe")

local ngx = ngx
local getenv = os.getenv

local _M = {}

local RULES = "micro_rules"
local POLL = 1 -- seconds: rules and registry refresh period

local conf = {}           -- bootstrap settings
local current, current_gen -- per worker compiled rules and their generation

local function split(v)
    local out = {}
    for s in (v or ""):gmatch("[^,]+") do
        s = s:gsub("^%s+", ""):gsub("%s+$", "")
        if s ~= "" then
            out[#out + 1] = s
        end
    end
    return out
end

-- load parses and stores a rules document for every worker. It returns
-- nil and the reason when the document is invalid.
local function load(text)
    local dict = ngx.shared[RULES]
    local gen = (dict:get("gen") or 0) + 1
    local rs, err = rules.parse(text, conf.registry.kind, gen)
    if not rs then
        return nil, err
    end
    dict:set("doc", text)
    dict:set("gen", gen)
    return true
end

-- init runs in the master: read bootstrap settings (SPEC 11) and refuse
-- to start on bad ones. A file rules document is checked here too; KV
-- sources can only be read once workers run (no cosockets in init).
function _M.init(opts)
    local kind = getenv("MICRO_REGISTRY") or ""
    if kind ~= "etcd" and kind ~= "consul" and kind ~= "nacos" then
        error("MICRO_REGISTRY must be etcd, consul or nacos, got '" .. kind .. "'")
    end
    conf.registry = {
        kind = kind,
        addrs = split(getenv("MICRO_REGISTRY_ADDRESS")),
        namespace = getenv("MICRO_REGISTRY_NAMESPACE"),
        group = getenv("MICRO_REGISTRY_GROUP"),
    }
    local trusted, err = ip.prefixes(split(getenv("MICRO_GATEWAY_TRUSTED_PROXIES")))
    if not trusted then
        error("MICRO_GATEWAY_TRUSTED_PROXIES: " .. err)
    end
    conf.trusted = trusted

    local ok
    ok, err = rules.load_schema(opts.schema)
    if not ok then
        error("rules schema: " .. tostring(err))
    end

    local uri = getenv("MICRO_GATEWAY_RULES") or ""
    if uri == "" then
        error("MICRO_GATEWAY_RULES is required (a rules source URI, or 'none')")
    end
    if uri == "none" then
        assert(load("version: 1"))
        return
    end
    conf.source, err = source.parse(uri)
    if not conf.source then
        error(err)
    end
    if conf.source.kind == "file" then
        local text
        text, err = source.read_file(conf.source)
        if not text then
            error("read rules: " .. tostring(err))
        end
        ok, err = load(text)
        if not ok then
            error(err)
        end
        ngx.shared[RULES]:set("raw", text)
    end
end

-- stop takes the whole gateway down: the rules are missing or invalid at
-- start-up (SPEC 10).
local function stop(reason)
    ngx.log(ngx.EMERG, "gateway: ", reason, "; stopping")
    -- docker-entrypoint.sh turns this marker into a non-zero exit
    local f = io.open("/tmp/micro-gateway-failed", "w")
    if f then
        f:write(reason, "\n")
        f:close()
    end
    local process = require("ngx.process")
    local signal = require("resty.signal")
    signal.kill(process.get_master_pid(), "QUIT")
end

local function poll_rules(premature)
    if premature or not conf.source then
        return
    end
    local dict = ngx.shared[RULES]
    local text, err = source.read(conf.source)
    if not text then
        if not dict:get("gen") then
            return stop("cannot read rules from " .. conf.source.kind .. ": " .. tostring(err))
        end
        ngx.log(ngx.WARN, "gateway: rules source: ", err)
        return
    end
    if text == dict:get("raw") then
        return
    end
    dict:set("raw", text)
    local ok
    ok, err = load(text)
    if ok then
        ngx.log(ngx.NOTICE, "gateway: rules reloaded from ", conf.source.kind)
    elseif not dict:get("gen") then
        return stop("invalid rules: " .. err)
    else
        ngx.log(ngx.ERR, "gateway: rejected rules update, keeping previous rules: ", err)
    end
end

local function poll_registry(premature)
    if not premature then
        discovery.poll()
    end
end

function _M.init_worker()
    discovery.init(conf.registry)
    if ngx.worker.id() ~= 0 then
        return
    end
    ngx.timer.at(0, poll_rules)
    ngx.timer.every(POLL, poll_rules)
    ngx.timer.every(POLL, poll_registry)
end

-- rule_set returns this worker's compiled copy of the current rules.
local function rule_set()
    local dict = ngx.shared[RULES]
    local gen = dict:get("gen")
    if not gen then
        return nil
    end
    if gen ~= current_gen then
        local rs, err = rules.parse(dict:get("doc"), conf.registry.kind, gen)
        if not rs then
            -- the document was validated before it was stored
            ngx.log(ngx.ERR, "gateway: compile stored rules: ", err)
            return current
        end
        current, current_gen = rs, gen
    end
    return current
end

local function access()
    local path = ngx.var.request_uri
    ngx.var.micro_method = path

    local derived, endpoint = rules.split(path)
    if not derived then
        return errors.malformed(path)
    end
    local rs = rule_set()
    if not rs then
        return errors.new(errors.UNAVAILABLE, 503, "gateway starting: rules not loaded")
    end
    local rt = rules.match(rs, path, derived)
    if not rt then
        return errors.no_route(path)
    end
    local service = rt.service or derived
    if service == "" then
        return errors.no_route(path)
    end
    ngx.var.micro_service = service
    ngx.var.micro_route = rt.name or ""

    local hdrs = ngx.req.get_headers(0)
    local call = {
        client_ip = headers.client_ip(ngx.var.remote_addr, hdrs["x-forwarded-for"], conf.trusted),
        headers = hdrs,
    }
    ngx.var.micro_client_ip = call.client_ip
    for _, p in ipairs(rs.global) do
        local err = p:check(call)
        if err then
            return err
        end
    end
    for _, p in ipairs(rt.plugins) do
        local err = p:check(call)
        if err then
            return err
        end
    end

    local services = selector.eligible(discovery.get(service), rt.filters, endpoint)
    if selector.count(services) == 0 then
        return errors.no_nodes(service)
    end
    local candidates = selector.candidates(rt, services)
    if #candidates == 0 then
        return errors.no_nodes(service)
    end

    ngx.var.micro_trace = headers.prepare(hdrs, call.client_ip, rt.name, call.account)
    ngx.ctx.micro = {
        candidates = candidates,
        route = rt,
        p2c = rt.selector.strategy == "p2c" and not rt.selector.version_weights,
    }
end

function _M.access()
    local err = access()
    if err then
        return errors.respond(err)
    end
end

-- balance hands nginx the next candidate on every try. Retries are
-- capped by the route and by the candidates left.
function _M.balance()
    local m = ngx.ctx.micro
    local rt = m.route
    if not m.try then
        m.try = 0
        local more = math.min(rt.retries, #m.candidates - 1)
        if more > 0 then
            balancer.set_more_tries(more)
        end
        balancer.set_timeouts(rt.connect, rt.send, rt.read)
    end
    m.try = m.try + 1
    local n = m.candidates[m.try]
    if not n then
        return ngx.exit(502)
    end
    if m.p2c then
        selector.p2c_begin(n.address)
        ngx.var.micro_p2c = (ngx.var.micro_p2c ~= "" and (ngx.var.micro_p2c .. ",") or "") .. n.address
    end
    local ok, err = balancer.set_current_peer(n.host, n.port)
    if not ok then
        ngx.log(ngx.ERR, "gateway: set peer ", n.address, ": ", err)
        return ngx.exit(502)
    end
end

-- log fills the access-log fields and ends every p2c try, observing the
-- latency of the last one. Field names match wrapper/logging, so gateway
-- and service lines join on trace_id.
function _M.log()
    local code = ngx.var.micro_grpc_status
    if code == "" then
        -- trailers, or headers for a trailers-only upstream reply
        code = ngx.var.upstream_trailer_grpc_status or ngx.var.upstream_http_grpc_status or ""
    end
    if code ~= "" then
        ngx.var.micro_code = errors.code_name(tonumber(code))
    end
    local trace = ngx.var.micro_trace:match("^00%-(%x+)%-")
    if trace then
        ngx.var.micro_trace_id = trace
    end

    local tried = ngx.var.micro_p2c
    if not tried or tried == "" then
        return
    end
    local times = {}
    for t in (ngx.var.upstream_response_time or ""):gmatch("[^,%s:]+") do
        times[#times + 1] = tonumber(t)
    end
    local addrs = {}
    for a in tried:gmatch("[^,]+") do
        addrs[#addrs + 1] = a
    end
    for i, a in ipairs(addrs) do
        selector.p2c_end(a, i == #addrs and times[#times] or nil)
    end
end

-- gateway_error sends the error access() chose (location @micro_error).
function _M.gateway_error()
    return errors.send()
end

-- upstream_error answers the errors nginx itself raises when no node
-- could take the call (error_page 502 and 504).
function _M.upstream_error(status)
    local service = ngx.var.micro_service ~= "" and ngx.var.micro_service or "upstream"
    if status == 504 then
        return errors.send(errors.upstream_timeout(service))
    end
    if status == 502 then
        return errors.send(errors.upstream_connect(service))
    end
    return errors.send(errors.internal("gateway error " .. tostring(status)))
end

return _M
