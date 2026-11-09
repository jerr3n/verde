-- SPDX-License-Identifier: MPL-2.0

--[[
	This script reads a built place or model with the Lune reader, as a real reader does.
	It prints the properties of each instance as wire values. It uses the Values module of the plugin for the encoding.
	build_test.go compares the output with the input of the build.

		lune run read_place.luau <Values.luau> <place.rbxlx or model.rbxmx> <wanted.json>

	wanted.json is a list of { id, path, class, props }. The list tells the script which properties to read,
	because most properties in a file are default values that the build did not write.
	Each path starts at the top of the file. The script stops with an error if an instance in the list is missing.
	The output also has the number of instances at the top of the file.
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

local game
if string.sub(placePath, -6) == ".rbxmx" then
	-- A model is a list of instances. Put them in one Folder, so that find can use the same steps as for a place.
	game = roblox.Instance.new("Folder")
	for _, instance in roblox.deserializeModel(fs.readFile(placePath)) do
		instance.Parent = game
	end
else
	game = roblox.deserializePlace(fs.readFile(placePath))
end

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

-- Map each full name to its id. A Ref comes back as an instance, and refOf must change it back into an id.
local ids: { [string]: string } = {}
for _, entry in wanted do
	ids[find(entry.path):GetFullName()] = entry.id
end
local function refOf(instance: any): string?
	return ids[instance:GetFullName()]
end

-- read returns the value of a property. For a migrated property, read returns the replacement property.
-- For example, Decal.Texture gives TextureContent.
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

print(serde.encode("json", { top = #game:GetChildren(), instances = results }))
