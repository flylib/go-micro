-- Minimal test runner, executed by t/runner.conf in init_by_lua (the
-- alpine image has no perl for the resty CLI). Each t/*_test.lua file
-- registers cases with T.test; failures are collected and summarised.

local T = { cases = {}, failures = 0 }
_G.T = T

local function out(...)
    io.stdout:write(table.concat({ ... }), "\n")
end

local function show(v)
    if type(v) == "string" then
        return string.format("%q", v)
    end
    return tostring(v)
end

function T.test(name, fn)
    T.cases[#T.cases + 1] = { name = name, fn = fn }
end

function T.eq(got, want, msg)
    if got ~= want then
        error(string.format("%s: got %s, want %s", msg or "values differ", show(got), show(want)), 2)
    end
end

function T.ok(cond, msg)
    if not cond then
        error(msg or "condition false", 2)
    end
end

local root = os.getenv("T_ROOT") or "/t"
assert(require("resty.micro.rules").load_schema(root .. "/lib/resty/micro/schema.json"))

local files = {}
local p = io.popen("ls " .. root .. "/t/*_test.lua")
for f in p:lines() do
    files[#files + 1] = f
end
p:close()

for _, f in ipairs(files) do
    T.cases = {}
    dofile(f)
    local name = f:match("([^/]+)%.lua$")
    for _, c in ipairs(T.cases) do
        local ok, err = pcall(c.fn)
        if ok then
            out("ok   ", name, " ", c.name)
        else
            T.failures = T.failures + 1
            out("FAIL ", name, " ", c.name, "\n     ", tostring(err))
        end
    end
end

out(T.failures == 0 and "PASS" or ("FAIL: " .. T.failures .. " case(s)"))
return T.failures == 0
