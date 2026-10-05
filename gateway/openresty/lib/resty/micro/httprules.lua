-- HTTP rules (SPEC 2.2): REST mappings after google.api.http, mirroring
-- gateway/proxy/httprules.go. A rule's path template is matched against
-- the raw request path; the request message is built from the body, the
-- path variables and the query, and sent as an application/grpc+json call
-- to the rule's target.
--
-- cjson decodes numbers to doubles, so before decoding a body, numbers
-- with more than 15 significant digits are turned into strings (proto3
-- JSON accepts strings for int64 and double fields); response_body is cut
-- from the reply text without decoding, so replies keep their exact form.

local cjson = require("cjson.safe").new()
cjson.encode_escape_forward_slash(false)
cjson.encode_number_precision(16)
cjson.decode_array_with_array_mt(true)

local errors = require("resty.micro.errors")

local sfind, ssub, sbyte, sort = string.find, string.sub, string.byte, table.sort
local concat = table.concat

local _M = {}

local LIT, STAR, DSTAR = 1, 2, 3

local function bad_request(detail)
    return errors.new(errors.INVALID_ARGUMENT, 400, detail)
end

local function literal_or_wildcard(s)
    if s == "*" then
        return { kind = STAR }
    elseif s == "**" then
        return { kind = DSTAR }
    elseif s == "" or sfind(s, "[{}*=]") then
        return nil, "bad segment " .. s
    end
    return { kind = LIT, literal = s }
end

-- parse parses "/" segment ["/" ...] [":" verb].
function _M.parse(t)
    if ssub(t, 1, 1) ~= "/" then
        return nil, "template must start with /"
    end
    local hr = { tokens = {}, vars = {}, literals = 0, dstars = 0 }
    local body = ssub(t, 2)

    -- a verb follows the last ':' outside braces, in the last segment
    local depth = 0
    for i = #body, 1, -1 do
        local c = ssub(body, i, i)
        if c == "}" then
            depth = depth + 1
        elseif c == "{" then
            depth = depth - 1
        elseif c == ":" and depth == 0 and not sfind(ssub(body, i), "/", 1, true) then
            hr.verb = ssub(body, i + 1)
            body = ssub(body, 1, i - 1)
            if hr.verb == "" then
                return nil, "empty verb"
            end
            break
        end
    end

    local function add(tk, var)
        tk.var = var
        if tk.kind == LIT then
            hr.literals = hr.literals + 1
        elseif tk.kind == DSTAR then
            hr.dstars = hr.dstars + 1
        end
        hr.tokens[#hr.tokens + 1] = tk
    end

    while true do
        local seg
        if ssub(body, 1, 1) == "{" then
            local e = sfind(body, "}", 1, true)
            if not e then
                return nil, "unclosed {"
            end
            seg, body = ssub(body, 1, e), ssub(body, e + 1)
        else
            local i = sfind(body, "/", 1, true)
            if i then
                seg, body = ssub(body, 1, i - 1), ssub(body, i)
            else
                seg, body = body, ""
            end
        end

        if seg == "" then
            return nil, "empty segment"
        elseif ssub(seg, 1, 1) == "{" then
            local inner = ssub(seg, 2, -2)
            local field, pattern = inner:match("^([^=]*)=(.*)$")
            if not field then
                field, pattern = inner, "*"
            end
            if field == "" or sfind(field, "[{}/*]") then
                return nil, "bad variable " .. seg
            end
            hr.vars[#hr.vars + 1] = field
            for p in (pattern .. "/"):gmatch("([^/]*)/") do
                local tk, err = literal_or_wildcard(p)
                if not tk then
                    return nil, "variable " .. field .. ": " .. err
                end
                add(tk, #hr.vars)
            end
        else
            local tk, err = literal_or_wildcard(seg)
            if not tk then
                return nil, err
            end
            add(tk, nil)
        end

        if body == "" then
            break
        end
        if ssub(body, 1, 1) ~= "/" then
            return nil, "unexpected " .. body .. " after a segment"
        end
        body = ssub(body, 2)
        if body == "" then
            return nil, "trailing /"
        end
    end

    for i, tk in ipairs(hr.tokens) do
        if tk.kind == DSTAR and i ~= #hr.tokens then
            return nil, "** must be the last element"
        end
    end
    return hr
end

-- compile parses every rule and orders them most specific first.
function _M.compile(specs)
    local out, seen = {}, {}
    for i, s in ipairs(specs or {}) do
        local key = s.method .. " " .. s.path
        if seen[key] then
            return nil, "http_rules: " .. key .. " defined twice"
        end
        seen[key] = true
        local hr, err = _M.parse(s.path)
        if not hr then
            return nil, "http_rules: " .. key .. ": " .. err
        end
        hr.method, hr.target = s.method, s.target
        hr.body, hr.response_body = s.body or "", s.response_body or ""
        hr.params = s.params or {}
        hr.has_params = next(hr.params) ~= nil
        hr.order = i
        out[#out + 1] = hr
    end
    sort(out, function(a, b)
        if a.literals ~= b.literals then
            return a.literals > b.literals
        end
        if a.dstars ~= b.dstars then
            return a.dstars < b.dstars
        end
        return a.order < b.order
    end)
    return out
end

-- match matches a raw request path, returning decoded variables.
function _M.match(hr, raw)
    local p = ssub(raw, 2)
    if hr.verb then
        local suffix = ":" .. hr.verb
        if ssub(p, -#suffix) ~= suffix then
            return nil
        end
        p = ssub(p, 1, -#suffix - 1)
    end
    local segs = {}
    if p ~= "" then
        for s in (p .. "/"):gmatch("([^/]*)/") do
            segs[#segs + 1] = s
        end
    end
    local bound = {}
    local i = 1
    for _, tk in ipairs(hr.tokens) do
        if tk.kind == LIT then
            if segs[i] ~= tk.literal then
                return nil
            end
            if tk.var then
                bound[tk.var] = bound[tk.var] or {}
                bound[tk.var][#bound[tk.var] + 1] = segs[i]
            end
            i = i + 1
        elseif tk.kind == STAR then
            if not segs[i] or segs[i] == "" then
                return nil
            end
            if tk.var then
                bound[tk.var] = bound[tk.var] or {}
                bound[tk.var][#bound[tk.var] + 1] = segs[i]
            end
            i = i + 1
        else -- DSTAR: the rest
            if tk.var then
                bound[tk.var] = bound[tk.var] or {}
                for j = i, #segs do
                    bound[tk.var][#bound[tk.var] + 1] = segs[j]
                end
            end
            i = #segs + 1
        end
    end
    if i ~= #segs + 1 then
        return nil
    end
    local vars = {}
    for v, field in ipairs(hr.vars) do
        vars[field] = ngx.unescape_uri(concat(bound[v] or {}, "/"))
    end
    return vars
end

-- find picks the most specific matching rule (SPEC 2.2).
function _M.find(rules, method, raw)
    for _, hr in ipairs(rules or {}) do
        if hr.method == method then
            local vars = _M.match(hr, raw)
            if vars then
                return hr, vars
            end
        end
    end
end

-- quote_big_numbers turns numbers with more than 15 significant digits,
-- outside strings, into JSON strings, so cjson keeps them exact.
local function quote_big_numbers(s)
    local out, i, n, last = {}, 1, #s, 1
    while i <= n do
        local c = sbyte(s, i)
        if c == 34 then -- string: skip to its closing quote
            i = i + 1
            while i <= n do
                local d = sbyte(s, i)
                if d == 92 then
                    i = i + 2
                elseif d == 34 then
                    break
                else
                    i = i + 1
                end
            end
            i = i + 1
        elseif c == 45 or (c >= 48 and c <= 57) then
            local j = i
            while j <= n do
                local d = sbyte(s, j)
                if not (d == 45 or d == 43 or d == 46 or d == 101 or d == 69 or (d >= 48 and d <= 57)) then
                    break
                end
                j = j + 1
            end
            local num = ssub(s, i, j - 1)
            local digits = num:gsub("[eE].*$", ""):gsub("[^0-9]", ""):gsub("^0+", "")
            if #digits > 15 then
                out[#out + 1] = ssub(s, last, i - 1)
                out[#out + 1] = '"' .. num .. '"'
                last = j
            end
            i = j
        else
            i = i + 1
        end
    end
    out[#out + 1] = ssub(s, last)
    return concat(out)
end

local function decode(s)
    local v = cjson.decode(quote_big_numbers(s))
    if v == nil then
        return nil, bad_request("request body is not valid JSON")
    end
    return v
end

local function is_object(v)
    return type(v) == "table" and getmetatable(v) ~= cjson.array_mt
end

-- set_path sets a dotted field path in a JSON object.
local function set_path(obj, path, v)
    local parts = {}
    for p in (path .. "."):gmatch("([^.]*)%.") do
        parts[#parts + 1] = p
    end
    for k = 1, #parts - 1 do
        local next_ = obj[parts[k]]
        if next_ == nil then
            next_ = {}
            obj[parts[k]] = next_
        elseif not is_object(next_) then
            return bad_request("field " .. path .. " conflicts with a non-object value at " .. parts[k])
        end
        obj = next_
    end
    obj[parts[#parts]] = v
end

local function convert(hr, field, vals)
    local typ = hr.params[field]
    local function one(s)
        if s == true then
            s = "" -- ?flag without a value
        end
        if typ ~= "bool" then
            return s
        end
        if s == "true" then
            return true
        elseif s == "false" then
            return false
        end
        return nil, bad_request("parameter " .. field .. " must be true or false, got \"" .. tostring(s) .. "\"")
    end
    if typ == "repeated" or #vals > 1 then
        local out = setmetatable({}, cjson.array_mt)
        for _, s in ipairs(vals) do
            local v, err = one(s)
            if err then
                return nil, err
            end
            out[#out + 1] = v
        end
        return out
    end
    return one(vals[1])
end

-- build makes the JSON request message (SPEC 2.2); query is the table of
-- ngx.req.get_uri_args.
function _M.build(hr, body, vars, query)
    local msg = {}
    local trimmed = (body or ""):match("^%s*(.-)%s*$")
    if hr.body == "*" then
        if trimmed ~= "" then
            local v, err = decode(trimmed)
            if err then
                return nil, err
            end
            if not is_object(v) then
                return nil, bad_request("request body must be a JSON object")
            end
            msg = v
        end
    elseif hr.body == "" then
        if trimmed ~= "" and trimmed ~= "{}" then
            return nil, bad_request("this endpoint takes no request body")
        end
    elseif trimmed ~= "" then
        local v, err = decode(trimmed)
        if err then
            return nil, err
        end
        err = set_path(msg, hr.body, v)
        if err then
            return nil, err
        end
    end

    for field, raw in pairs(vars) do
        local v, err = convert(hr, field, { raw })
        if err then
            return nil, err
        end
        err = set_path(msg, field, v)
        if err then
            return nil, err
        end
    end

    if hr.body ~= "*" then
        for key, val in pairs(query or {}) do
            if vars[key] == nil and (not hr.has_params or hr.params[key]) then
                local vals = type(val) == "table" and val or { val }
                local v, err = convert(hr, key, vals)
                if err then
                    return nil, err
                end
                err = set_path(msg, key, v)
                if err then
                    return nil, err
                end
            end
        end
    end
    local out = cjson.encode(msg)
    if not out then
        return nil, bad_request("request message cannot be encoded")
    end
    return out
end

-- skip_value returns the index after the JSON value starting at i.
local function skip_ws(s, i)
    return (sfind(s, "[^ \t\r\n]", i)) or #s + 1
end

local function skip_value(s, i)
    local c = ssub(s, i, i)
    if c == '"' then
        local j = i + 1
        while j <= #s do
            local d = ssub(s, j, j)
            if d == "\\" then
                j = j + 2
            elseif d == '"' then
                return j + 1
            else
                j = j + 1
            end
        end
        return nil
    elseif c == "{" or c == "[" then
        local depth, j = 0, i
        while j <= #s do
            local d = ssub(s, j, j)
            if d == '"' then
                j = skip_value(s, j)
                if not j then
                    return nil
                end
            else
                if d == "{" or d == "[" then
                    depth = depth + 1
                elseif d == "}" or d == "]" then
                    depth = depth - 1
                    if depth == 0 then
                        return j + 1
                    end
                end
                j = j + 1
            end
        end
        return nil
    end
    local j = sfind(s, "[,}%] \t\r\n]", i)
    return j or #s + 1
end

-- field_raw returns the raw text of key in the JSON object text s.
local function field_raw(s, key)
    local i = skip_ws(s, 1)
    if ssub(s, i, i) ~= "{" then
        return nil
    end
    i = skip_ws(s, i + 1)
    while i <= #s and ssub(s, i, i) ~= "}" do
        local kend = skip_value(s, i)
        if not kend then
            return nil
        end
        local k = cjson.decode(ssub(s, i, kend - 1))
        i = skip_ws(s, kend)
        if ssub(s, i, i) ~= ":" then
            return nil
        end
        i = skip_ws(s, i + 1)
        local vend = skip_value(s, i)
        if not vend then
            return nil
        end
        if k == key then
            return ssub(s, i, vend - 1)
        end
        i = skip_ws(s, vend)
        if ssub(s, i, i) == "," then
            i = skip_ws(s, i + 1)
        end
    end
    return nil
end

-- reply applies response_body to the JSON reply text, keeping its form.
function _M.reply(hr, text)
    if hr.response_body == "" then
        return text
    end
    local v = text
    for p in (hr.response_body .. "."):gmatch("([^.]*)%.") do
        v = field_raw(v, p)
        if not v then
            return "null" -- unset fields are omitted by protojson
        end
    end
    return v
end

return _M
