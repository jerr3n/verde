--[[
	Reads a built place the way a real reader would, and prints each instance's properties
	as wire values, using the plugin's own Values module. build_test.go compares those with
	what went in.

		lune run read_place.luau <Values.luau> <place.rbxlx> <wanted.json>

	wanted.json is a list of { id, path, class, props }. It says what to look at, since most
	properties in a place are defaults that were never written.
]]

local fs = require("@lune/fs")
local luau = require("@lune/luau")
local process = require("@lune/process")
local roblox = require("@lune/roblox")
local serde = require("@lune/serde")

local valuesPath, placePath, wantedPath = process.args[1], process.args[2], process.args[3]

local environment = table.clone(roblox) :: any
environment.game = {
	GetService = function()
		return {
			JSONEncode = function(_, value)
				return serde.encode("json", value)
			end,
		}
	end,
}
local Values = luau.load(fs.readFile(valuesPath), { debugName = "Values", environment = environment })()

local game = roblox.deserializePlace(fs.readFile(placePath))

local function find(path: { string }): any
	local current = game
	for _, name in path do
		local found = nil
		for _, child in current:GetChildren() do
			if child.Name == name then
				found = child
			end
		end
		assert(found ~= nil, `{table.concat(path, "/")}: nothing called {name}`)
		current = found
	end
	return current
end

local wanted = serde.decode("json", fs.readFile(wantedPath))

-- By full name, since a Ref comes back as an instance and has to be turned back into an id.
local ids: { [string]: string } = {}
for _, entry in wanted do
	ids[find(entry.path):GetFullName()] = entry.id
end
local function refOf(instance: any): string?
	return ids[instance:GetFullName()]
end

-- Reading a migrated property gives its replacement, like Decal.Texture to TextureContent.
local function read(instance: any, name: string): any
	local ok, value = pcall(function()
		return instance[name]
	end)
	if ok and value ~= nil then
		return value
	end
	local replaced = (if name == "BrickColor" then "Color" else name .. "Content")
	local okReplaced, replacement = pcall(function()
		return instance[replaced]
	end)
	return if okReplaced then replacement else nil
end

local results = {}
for _, entry in wanted do
	local instance = find(entry.path)
	local props = {}
	for name in entry.props do
		if name == "$tags" then
			local tags = instance:GetTags()
			table.sort(tags)
			props[name] = { t = "Tags", v = tags }
		elseif name == "$attributes" then
			local pairs = {}
			for key, value in instance:GetAttributes() do
				table.insert(pairs, { key, Values.encode(value, refOf) })
			end
			table.sort(pairs, function(a, b)
				return a[1] < b[1]
			end)
			props[name] = { t = "Attributes", v = pairs }
		else
			local encoded = Values.encode(read(instance, name), refOf)
			props[name] = encoded or { t = "unreadable" }
		end
	end
	table.insert(results, { id = entry.id, class = instance.ClassName, props = props })
end

print(serde.encode("json", results))
