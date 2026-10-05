-- Request headers the gateway reads and rewrites (SPEC 7).

local ip = require("resty.micro.ip")
local random = require("resty.random")
local rstring = require("resty.string")

local ngx = ngx
local sfind, ssub, sgmatch = string.find, string.sub, string.gmatch
local concat = table.concat

local _M = {}

local RESERVED = "micro-gateway-"

local function trim(s)
    return (s:gsub("^%s+", ""):gsub("%s+$", ""))
end

local function joined(v)
    if type(v) == "table" then
        return concat(v, ", ")
    end
    return v
end

-- valid_traceparent accepts W3C version-00 values with non-zero ids.
function _M.valid_traceparent(v)
    if type(v) ~= "string" then
        return false
    end
    local trace, span = v:match("^00%-(%x+)%-(%x+)%-%x%x$")
    if not trace or #trace ~= 32 or #span ~= 16 then
        return false
    end
    if v:find("[A-F]") then
        return false
    end
    return trace:find("[^0]") ~= nil and span:find("[^0]") ~= nil
end

-- new_traceparent starts a sampled trace for a call that arrived without one.
function _M.new_traceparent()
    local b = random.bytes(24, true) or random.bytes(24)
    local hex = rstring.to_hex(b)
    return "00-" .. ssub(hex, 1, 32) .. "-" .. ssub(hex, 33, 48) .. "-01"
end

-- client_ip is the peer address, unless the peer is a trusted proxy: then
-- the right-most x-forwarded-for entry that is not itself trusted.
function _M.client_ip(peer, xff, trusted)
    if not trusted or #trusted == 0 then
        return peer
    end
    local p = ip.parse(peer)
    if not p or not ip.in_any(trusted, p) then
        return peer
    end
    local hops = {}
    for h in sgmatch(joined(xff) or "", "[^,]+") do
        hops[#hops + 1] = trim(h)
    end
    for i = #hops, 1, -1 do
        local a = ip.parse(hops[i])
        if hops[i] ~= "" and not (a and ip.in_any(trusted, a)) then
            return hops[i]
        end
    end
    return peer
end

-- prepare rewrites the inbound headers for the upstream call: reserved
-- micro-gateway-* headers are replaced by the gateway's own, the client
-- is appended to x-forwarded-for, and a trace is started if none is
-- valid. It returns the effective traceparent.
function _M.prepare(hdrs, client, route, account)
    for k in pairs(hdrs) do
        if ssub(k, 1, #RESERVED) == RESERVED then
            ngx.req.clear_header(k)
        end
    end
    if route and route ~= "" then
        ngx.req.set_header("micro-gateway-route", route)
    end
    if account and account ~= "" then
        ngx.req.set_header("micro-gateway-account", account)
    end

    local xff = joined(hdrs["x-forwarded-for"])
    ngx.req.set_header("x-forwarded-for", (xff and xff ~= "") and (xff .. ", " .. client) or client)

    local tp = hdrs["traceparent"]
    if type(tp) == "table" then
        tp = tp[1]
    end
    if not _M.valid_traceparent(tp) then
        tp = _M.new_traceparent()
        ngx.req.set_header("traceparent", tp)
    end
    return tp
end

return _M
