-- The HTTP/JSON entry (SPEC 2.1): POST /api/<service>/<Handler>/<Method>
-- with a JSON body becomes the gRPC call /<service>.<Handler>/<Method>.
--
-- The call is proxied by grpc_pass in the HTTP request itself, so it gets
-- the gRPC entry's balancer and retries unchanged (grpc_pass inside a Lua
-- subrequest crashes nginx workers, so the reply cannot be buffered via
-- ngx.location.capture):
--
--   access()         check the request, route it like a gRPC call
--                    (gateway.route), wrap the body in a gRPC frame,
--                    point the URI at the gRPC method
--   header_filter()  pick the HTTP status: go-micro errors arrive
--                    trailers-only, so their status is in the headers
--   body_filter()    unwrap the reply frame, or write the error JSON
--
-- The HTTP status is chosen when the headers pass, so a reply that sends a
-- message and then a non-OK status keeps 200; go-micro's unary handlers
-- either reply or fail trailers-only, never both.

local gateway = require("resty.micro.gateway")
local errors = require("resty.micro.errors")
local cjson = require("cjson.safe")

local ngx = ngx
local sbyte, schar, ssub, lower = string.byte, string.char, string.sub, string.lower
local floor, concat = math.floor, table.concat

local _M = {}

local MAX_BODY = 4 * 1024 * 1024

-- the gRPC code -> HTTP status mapping go-micro's gRPC client uses
local HTTP_OF_GRPC = {
    [0] = 200, [3] = 400, [16] = 401, [7] = 403, [5] = 404, [4] = 408,
    [6] = 409, [9] = 412, [8] = 429, [12] = 501, [14] = 503,
}

local STATUS_TEXT = {
    [400] = "Bad Request", [401] = "Unauthorized", [403] = "Forbidden", [404] = "Not Found",
    [405] = "Method Not Allowed", [408] = "Request Timeout", [409] = "Conflict",
    [412] = "Precondition Failed", [413] = "Request Entity Too Large",
    [415] = "Unsupported Media Type", [429] = "Too Many Requests",
    [500] = "Internal Server Error", [501] = "Not Implemented", [502] = "Bad Gateway",
    [503] = "Service Unavailable", [504] = "Gateway Timeout",
}

-- micro_json renders a go-micro error with Go's field order.
local function micro_json(id, code, detail)
    return string.format('{"id":%s,"code":%d,"detail":%s,"status":%s}',
        cjson.encode(id), code, cjson.encode(detail), cjson.encode(STATUS_TEXT[code] or ""))
end

-- write ends the request with a JSON reply, from access or content.
local function write(status, body, code_name)
    ngx.var.micro_code = code_name or ""
    ngx.status = status
    ngx.header["Content-Type"] = "application/json"
    ngx.print(body)
    return ngx.exit(ngx.HTTP_OK)
end

local function refuse(status, detail)
    return write(status, micro_json("micro.gateway", status, detail), "")
end

local function fail(err)
    return write(err.http, errors.json(err.http, err.detail), errors.code_name(err.grpc))
end

local function be32(n)
    return schar(floor(n / 16777216) % 256, floor(n / 65536) % 256, floor(n / 256) % 256, n % 256)
end

local function read_be32(s, i)
    local a, b, c, d = sbyte(s, i, i + 3)
    return ((a * 256 + b) * 256 + c) * 256 + d
end

local function json_content_type(ct)
    if not ct then
        return true
    end
    return lower(ct):match("^%s*([^;%s]+)") == "application/json"
end

local function read_body()
    ngx.req.read_body()
    local body = ngx.req.get_body_data()
    if body then
        return body
    end
    local path = ngx.req.get_body_file()
    if not path then
        return ""
    end
    local f = io.open(path, "rb")
    if not f then
        return nil
    end
    body = f:read("*a")
    f:close()
    return body
end

-- access checks the request and turns it into the gRPC call.
function _M.access()
    local path = ngx.var.uri
    local service, handler, method = path:match("^/api/([^/]+)/([^/]+)/([^/]+)$")
    if not service then
        return refuse(404, "no API endpoint at " .. path .. ": want /api/<service>/<Handler>/<Method>")
    end
    if ngx.req.get_method() ~= "POST" then
        ngx.header["Allow"] = "POST"
        return refuse(405, "method " .. ngx.req.get_method() .. " not allowed: use POST")
    end
    local ct = ngx.var.content_type
    if not json_content_type(ct) then
        return refuse(415, "content type " .. ct .. " not supported: use application/json")
    end
    local body = read_body()
    if not body then
        return fail(errors.internal("read request body"))
    end
    if #body > MAX_BODY then
        return refuse(413, "request body larger than 4 MiB")
    end
    if body == "" then
        body = "{}"
    end

    local grpc_method = "/" .. service .. "." .. handler .. "/" .. method
    local err = gateway.route(grpc_method)
    if err then
        return fail(err)
    end
    ngx.ctx.micro.http = true
    ngx.req.set_uri(grpc_method)
    ngx.req.set_body_data("\0" .. be32(#body) .. body)
    ngx.req.set_header("Content-Type", "application/grpc+json")
    ngx.req.clear_header("Accept-Encoding")
end

-- header_filter turns the gRPC reply headers into the HTTP reply's.
function _M.header_filter()
    local m = ngx.ctx.micro
    if not m or not m.http then
        return
    end
    local code = tonumber(ngx.header["grpc-status"] or "")
    if code and code ~= 0 then
        -- trailers-only error: the status is already known
        local detail = ngx.unescape_uri(ngx.header["grpc-message"] or "")
        local e = cjson.decode(detail)
        local status, body
        if type(e) == "table" and type(e.code) == "number" and e.code >= 100 and e.code <= 599 then
            status, body = e.code, detail
        else
            status = HTTP_OF_GRPC[code] or 500
            body = micro_json(m.service, status, detail)
        end
        ngx.status = status
        ngx.var.micro_code = errors.code_name(code)
        m.replace = body
    else
        m.unwrap = {}
    end
    ngx.header["Content-Type"] = "application/json"
    ngx.header["Content-Length"] = nil
    ngx.header["grpc-status"] = nil
    ngx.header["grpc-message"] = nil
    ngx.header["grpc-encoding"] = nil
    ngx.header["grpc-accept-encoding"] = nil
end

-- body_filter unwraps the reply frame, or writes the error JSON.
function _M.body_filter()
    local m = ngx.ctx.micro
    if not m or not m.http then
        return
    end
    local chunk, eof = ngx.arg[1], ngx.arg[2]
    if m.replace then
        ngx.arg[1] = eof and m.replace or ""
        return
    end
    if not m.unwrap then
        return
    end
    m.unwrap[#m.unwrap + 1] = chunk
    if not eof then
        ngx.arg[1] = ""
        return
    end
    local body = concat(m.unwrap)
    if #body >= 5 and sbyte(body, 1) == 0 then
        local n = read_be32(body, 2)
        if #body == 5 + n then
            ngx.arg[1] = ssub(body, 6)
            return
        end
    end
    -- status 200 is already out; say what went wrong in the body
    ngx.log(ngx.ERR, "gateway: HTTP entry got an unexpected reply body from ", m.service, " (", #body, " bytes)")
    ngx.arg[1] = errors.json(500, "unexpected upstream reply: unary JSON reply expected")
end

-- upstream_error answers errors nginx itself raised (error_page 502/504).
function _M.upstream_error(status)
    local service = ngx.var.micro_service ~= "" and ngx.var.micro_service or "upstream"
    if status == 504 then
        return fail(errors.upstream_timeout(service))
    end
    if status == 502 then
        return fail(errors.upstream_connect(service))
    end
    return fail(errors.internal("gateway error " .. tostring(status)))
end

-- too_large answers bodies nginx rejected before access (error_page 413).
function _M.too_large()
    return refuse(413, "request body larger than 4 MiB")
end

return _M
