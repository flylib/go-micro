-- Standard plugins (SPEC 9.5): ip-restriction, jwt-auth, rate-limit.
-- A plugin is {check = function(self, call) return err_or_nil end}; call
-- carries client_ip, headers and account (set by jwt-auth).

local errors = require("resty.micro.errors")
local ip = require("resty.micro.ip")
local cjson = require("cjson.safe")
local b64 = require("ngx.base64")
local pkey = require("resty.openssl.pkey")
local limit_req = require("resty.limit.req")

local ngx = ngx
local sfind, ssub, lower = string.find, string.sub, string.lower

local _M = {}

local LIMIT_DICT = "micro_limit"
local JWT_LEEWAY = 30

local function header(call, name)
    local v = call.headers[name]
    if type(v) == "table" then
        v = v[1]
    end
    return v
end

-- ip-restriction: deny first, then allow if allow is set.

local ip_restriction = {}
ip_restriction.__index = ip_restriction

local function new_ip_restriction(cfg)
    local allow, err = ip.prefixes(cfg.allow)
    if not allow then
        return nil, err
    end
    local deny
    deny, err = ip.prefixes(cfg.deny)
    if not deny then
        return nil, err
    end
    return setmetatable({ allow = allow, deny = deny }, ip_restriction)
end

function ip_restriction:check(call)
    local addr = ip.parse(call.client_ip)
    if not addr then
        return errors.forbidden("client address unknown")
    end
    if ip.in_any(self.deny, addr) then
        return errors.forbidden("client address denied")
    end
    if #self.allow > 0 and not ip.in_any(self.allow, addr) then
        return errors.forbidden("client address not allowed")
    end
end

-- jwt-auth: RS256 tokens as issued by auth/jwt.

local jwt_auth = {}
jwt_auth.__index = jwt_auth

local function new_jwt_auth(cfg)
    -- same encoding as auth/jwt/token.WithPublicKey: base64 of PEM
    local pem = ngx.decode_base64(cfg.public_key or "")
    if not pem or not sfind(pem, "-----BEGIN", 1, true) then
        return nil, "public_key: not base64-encoded PEM"
    end
    local key, err = pkey.new(pem, { format = "PEM", type = "pu" })
    if not key then
        return nil, "public_key: " .. tostring(err)
    end
    if key:get_key_type().sn ~= "rsaEncryption" then
        return nil, "public_key: not an RSA key"
    end
    return setmetatable({ key = key, scopes = cfg.scopes or {}, forward = cfg.forward_claims }, jwt_auth)
end

local function decode_segment(seg)
    local raw = b64.decode_base64url(seg)
    if not raw then
        return nil
    end
    return cjson.decode(raw)
end

function jwt_auth:verify(token)
    local h, p, s = token:match("^([^.]+)%.([^.]+)%.([^.]+)$")
    if not h then
        return nil, "malformed"
    end
    local hdr = decode_segment(h)
    -- RS256 only: trusting the token's alg enables HS256 forgeries made
    -- with the public key
    if type(hdr) ~= "table" or hdr.alg ~= "RS256" then
        return nil, "alg not allowed"
    end
    local sig = b64.decode_base64url(s)
    if not sig then
        return nil, "malformed signature"
    end
    local ok = self.key:verify(sig, h .. "." .. p, "sha256")
    if not ok then
        return nil, "bad signature"
    end
    local claims = decode_segment(p)
    if type(claims) ~= "table" then
        return nil, "malformed claims"
    end
    local now = ngx.time()
    if type(claims.exp) ~= "number" or now > claims.exp + JWT_LEEWAY then
        return nil, "expired"
    end
    if type(claims.nbf) == "number" and now + JWT_LEEWAY < claims.nbf then
        return nil, "not yet valid"
    end
    return claims
end

local function any_scope(have, want)
    if type(have) ~= "table" then
        return false
    end
    for _, w in ipairs(want) do
        for _, h in ipairs(have) do
            if h == w then
                return true
            end
        end
    end
    return false
end

function jwt_auth:check(call)
    local auth = header(call, "authorization") or ""
    local token = auth:match("^Bearer (.+)$")
    if not token then
        return errors.unauthenticated("missing bearer token")
    end
    local claims, err = self:verify(token)
    if not claims then
        return errors.unauthenticated("invalid token: " .. err)
    end
    if #self.scopes > 0 and not any_scope(claims.scopes, self.scopes) then
        return errors.forbidden("token lacks a required scope")
    end
    call.account = type(claims.sub) == "string" and claims.sub or ""
    -- forward_claims: metadata key -> claim (SPEC 9.5)
    for key, name in pairs(self.forward or {}) do
        local v = claims[name]
        if type(v) == "string" or type(v) == "number" then
            call.forward = call.forward or {}
            call.forward[key] = tostring(v)
        end
    end
end

-- rate-limit: token bucket per key, capacity 1+burst. resty.limit.req
-- with its delay ignored behaves exactly so (nginx limit_req nodelay).
-- Buckets are shared by the workers of this gateway; the key carries the
-- plugin's id, which changes on every rules reload, as in the Go gateway.

local rate_limit = {}
rate_limit.__index = rate_limit

local function new_rate_limit(cfg, uid)
    local lim, err = limit_req.new(LIMIT_DICT, cfg.rate, cfg.burst or 0)
    if not lim then
        return nil, err
    end
    local key = cfg.key
    local hdr
    if ssub(key, 1, 7) == "header:" then
        hdr = lower(ssub(key, 8))
    end
    return setmetatable({ lim = lim, key = key, header = hdr, uid = uid }, rate_limit)
end

function rate_limit:keyof(call)
    if self.key == "account" and call.account and call.account ~= "" then
        return "account:" .. call.account
    end
    if self.header then
        local v = header(call, self.header)
        if v and v ~= "" then
            return "header:" .. v
        end
    end
    return "ip:" .. call.client_ip
end

function rate_limit:check(call)
    local delay, err = self.lim:incoming(self.uid .. ":" .. self:keyof(call), true)
    if not delay then
        if err == "rejected" then
            return errors.rate_limited()
        end
        ngx.log(ngx.ERR, "rate-limit: ", err)
    end
end

local builders = {
    ["ip-restriction"] = new_ip_restriction,
    ["jwt-auth"] = new_jwt_auth,
    ["rate-limit"] = new_rate_limit,
}

-- build turns plugin specs into plugins. Unknown names, x- ones
-- included, refuse the rules (SPEC 9.4). uid makes per-instance state
-- (rate-limit buckets) unique.
function _M.build(specs, uid)
    local out = {}
    for i, spec in ipairs(specs or {}) do
        local b = builders[spec.name]
        if not b then
            return nil, "plugin " .. tostring(spec.name) .. ": unknown plugin"
        end
        local p, err = b(spec.config or {}, uid .. "." .. i)
        if not p then
            return nil, "plugin " .. spec.name .. ": " .. tostring(err)
        end
        out[#out + 1] = p
    end
    return out
end

return _M
