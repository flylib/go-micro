-- IPv4/IPv6 addresses and CIDR prefixes, via libc inet_pton.

local ffi = require("ffi")

ffi.cdef[[
int inet_pton(int af, const char *src, void *dst);
]]

local C = ffi.C
local AF_INET = 2
local AF_INET6 = (ffi.os == "OSX") and 30 or 10
local buf = ffi.new("unsigned char[16]")

local sbyte, ssub, sfind = string.byte, string.sub, string.find
local floor = math.floor

local _M = {}

local V4_MAPPED = "\0\0\0\0\0\0\0\0\0\0\255\255"

-- parse returns the address as raw bytes (4 or 16), IPv4-mapped IPv6
-- unmapped to 4, or nil.
function _M.parse(s)
    if type(s) ~= "string" or s == "" then
        return nil
    end
    if C.inet_pton(AF_INET, s, buf) == 1 then
        return ffi.string(buf, 4)
    end
    if C.inet_pton(AF_INET6, s, buf) == 1 then
        local b = ffi.string(buf, 16)
        if ssub(b, 1, 12) == V4_MAPPED then
            return ssub(b, 13)
        end
        return b
    end
    return nil
end

-- prefix parses "a.b.c.d/n", "x::/n" or a bare address (full length).
function _M.prefix(s)
    local slash = sfind(s, "/", 1, true)
    local addr, bits = s, nil
    if slash then
        addr, bits = ssub(s, 1, slash - 1), tonumber(ssub(s, slash + 1))
    end
    local b = _M.parse(addr)
    if not b then
        return nil, "invalid address " .. s
    end
    local max = #b * 8
    bits = bits or max
    if bits < 0 or bits > max or bits ~= floor(bits) then
        return nil, "invalid prefix length in " .. s
    end
    return { bytes = b, bits = bits }
end

-- contains reports whether raw address bytes fall within prefix p.
function _M.contains(p, addr)
    if #addr ~= #p.bytes then
        return false
    end
    local full = floor(p.bits / 8)
    if full > 0 and ssub(addr, 1, full) ~= ssub(p.bytes, 1, full) then
        return false
    end
    local rest = p.bits % 8
    if rest == 0 then
        return true
    end
    -- compare the top `rest` bits of the next byte
    local shift = 2 ^ (8 - rest)
    local a, b = sbyte(addr, full + 1), sbyte(p.bytes, full + 1)
    return floor(a / shift) == floor(b / shift)
end

-- prefixes parses a list, failing on the first bad entry.
function _M.prefixes(list)
    local out = {}
    for _, s in ipairs(list or {}) do
        local p, err = _M.prefix(s)
        if not p then
            return nil, err
        end
        out[#out + 1] = p
    end
    return out
end

function _M.in_any(list, addr)
    for _, p in ipairs(list) do
        if _M.contains(p, addr) then
            return true
        end
    end
    return false
end

return _M
