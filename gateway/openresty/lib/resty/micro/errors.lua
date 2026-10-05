-- Gateway-originated errors (SPEC 8): a gRPC status whose message is a
-- go-micro error, the shape services produce.
--
-- A response nginx generates cannot end the HTTP/2 stream with headers
-- alone, so the status travels as real trailers set by
--   add_trailer grpc-status  $micro_grpc_status  always;
--   add_trailer grpc-message $micro_grpc_message always;
-- Those directives live only in the error locations, never where calls
-- are proxied: add_trailer makes nginx expect trailers on every response
-- of its location, which breaks the trailers-only replies upstreams send
-- for errors (the status would be lost).

local cjson = require("cjson.safe").new()
cjson.encode_escape_forward_slash(false)

local ngx = ngx
local sformat = string.format
local sbyte = string.byte

local _M = {}

-- gRPC status codes used by the gateway.
_M.INVALID_ARGUMENT = 3
_M.DEADLINE_EXCEEDED = 4
_M.NOT_FOUND = 5
_M.PERMISSION_DENIED = 7
_M.RESOURCE_EXHAUSTED = 8
_M.UNIMPLEMENTED = 12
_M.INTERNAL = 13
_M.UNAVAILABLE = 14
_M.UNAUTHENTICATED = 16

local STATUS_TEXT = {
    [400] = "Bad Request",
    [401] = "Unauthorized",
    [403] = "Forbidden",
    [404] = "Not Found",
    [429] = "Too Many Requests",
    [500] = "Internal Server Error",
    [501] = "Not Implemented",
    [502] = "Bad Gateway",
    [503] = "Service Unavailable",
    [504] = "Gateway Timeout",
}

local CODE_NAME = {
    [0] = "OK", [3] = "InvalidArgument", [4] = "DeadlineExceeded", [5] = "NotFound", [7] = "PermissionDenied",
    [8] = "ResourceExhausted", [12] = "Unimplemented", [13] = "Internal",
    [14] = "Unavailable", [16] = "Unauthenticated",
}

-- json renders errors.Error with Go's field order.
function _M.json(http_code, detail)
    return sformat('{"id":"micro.gateway","code":%d,"detail":%s,"status":%s}',
        http_code, cjson.encode(detail), cjson.encode(STATUS_TEXT[http_code] or ""))
end

-- percent_encode encodes a grpc-message value as the gRPC spec requires:
-- '%' and every byte outside printable ASCII.
function _M.percent_encode(s)
    return (s:gsub("[^\32-\36\38-\126]", function(c)
        return sformat("%%%02X", sbyte(c))
    end))
end

function _M.code_name(code)
    return CODE_NAME[code] or tostring(code)
end

-- new builds an error value; plugins and lookups return these.
function _M.new(grpc_code, http_code, detail)
    return { grpc = grpc_code, http = http_code, detail = detail }
end

local function set(err)
    ngx.var.micro_grpc_status = tostring(err.grpc)
    ngx.var.micro_grpc_message = _M.percent_encode(_M.json(err.http, err.detail))
end

-- respond ends the call with err, from a phase of the proxying location:
-- it hands over to @micro_error, whose add_trailer carries the status.
function _M.respond(err)
    set(err)
    return ngx.exec("@micro_error")
end

-- send writes the error response; for locations that declare add_trailer.
-- With err nil it sends the status already in the variables.
function _M.send(err)
    if err then
        set(err)
    end
    ngx.status = 200
    ngx.header["Content-Type"] = "application/grpc"
    return ngx.exit(ngx.HTTP_OK)
end

function _M.no_route(path)
    return _M.new(_M.UNIMPLEMENTED, 501, "no route for " .. path)
end

-- malformed matches what grpc-go answers for paths it rejects (SPEC 3.1).
function _M.malformed(path)
    return _M.new(_M.UNIMPLEMENTED, 501, "malformed method path " .. path)
end

function _M.unauthenticated(detail)
    return _M.new(_M.UNAUTHENTICATED, 401, detail)
end

function _M.forbidden(detail)
    return _M.new(_M.PERMISSION_DENIED, 403, detail)
end

function _M.rate_limited()
    return _M.new(_M.RESOURCE_EXHAUSTED, 429, "rate limit exceeded")
end

function _M.no_nodes(service)
    return _M.new(_M.UNAVAILABLE, 503, "no available nodes for " .. service)
end

function _M.upstream_connect(service)
    return _M.new(_M.UNAVAILABLE, 502, "could not reach " .. service)
end

function _M.upstream_timeout(service)
    return _M.new(_M.DEADLINE_EXCEEDED, 504, "upstream " .. service .. " timed out")
end

function _M.internal(detail)
    return _M.new(_M.INTERNAL, 500, detail)
end

return _M
