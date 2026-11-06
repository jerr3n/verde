--[[
	Reads and writes an instance's synced properties as wire values.

	A synced property is one ReflectionService says is saved with the place, plus
	three special cases: script Source, $attributes and $tags.
]]

local ReflectionService = game:GetService("ReflectionService")
local ScriptEditorService = game:GetService("ScriptEditorService")

local Values = require(script.Parent.Values)

type Wire = Values.Wire
type RefOf = (Instance) -> string?
type Resolve = (string) -> Instance?

local Properties = {}

Properties.SOURCE = "Source"
Properties.ATTRIBUTES = "$attributes"
Properties.TAGS = "$tags"

-- Handled separately, or meaningless to sync.
local SKIPPED: { [string]: boolean } = {
	Parent = true,
	Name = true,
	ClassName = true,
	Source = true,
	Archivable = true,
}

local namesByClass: { [string]: { string } } = {}
local defaultsByClass: { [string]: { [string]: string } } = {}

--[[
	Names of the regular properties to sync for a class. If reflection fails, this
	returns an empty list, so scripts still sync through Source.
]]
function Properties.names(className: string): { string }
	local cached = namesByClass[className]
	if cached then
		return cached
	end

	local names = {}
	local ok, result = pcall(function()
		return ReflectionService:GetPropertiesOfClass(className)
	end)
	if ok then
		for _, property in result :: any do
			local display = property.Display
			if property.Serialized and display and not display.DeprecationMessage and not SKIPPED[property.Name] then
				table.insert(names, property.Name)
			end
		end
	else
		warn(`[Verde] couldn't list properties of {className}: {result}`)
	end
	table.sort(names)

	namesByClass[className] = names
	return names
end

local function findDocument(instance: any): any
	local ok, document = pcall(function()
		return ScriptEditorService:FindScriptDocument(instance)
	end)
	return if ok then document else nil
end

local function readSource(instance: any): string
	-- Only trusted for open scripts: the docs don't say what it returns otherwise,
	-- and an empty result here could wipe the script's file.
	if findDocument(instance) then
		local ok, source = pcall(function()
			return ScriptEditorService:GetEditorSource(instance)
		end)
		if ok and type(source) == "string" then
			return source
		end
	end
	return instance.Source
end

local function writeSource(instance: any, source: string)
	-- A script open in the editor has to change through its document, or the
	-- editor keeps showing the old text.
	if instance:IsDescendantOf(game) and findDocument(instance) then
		local updated = pcall(function()
			ScriptEditorService:UpdateSourceAsync(instance, function()
				return source
			end)
		end)
		if updated then
			return
		end
	end
	instance.Source = source
end

local function readAttributes(instance: Instance, refOf: RefOf): Wire
	local attributes = instance:GetAttributes()
	local names = {}
	for name in attributes do
		table.insert(names, name)
	end
	table.sort(names)

	local out = {}
	for _, name in names do
		local wire = Values.encode(attributes[name], refOf)
		if wire then
			table.insert(out, { name, wire })
		end
	end
	return { t = "Attributes", v = out }
end

local function writeAttributes(instance: Instance, pairs: { any }, resolve: Resolve)
	local wanted = {}
	for _, pair in pairs do
		local ok, value = Values.decode(pair[2], resolve)
		if ok then
			wanted[pair[1]] = value
			instance:SetAttribute(pair[1], value)
		end
	end
	for name in instance:GetAttributes() do
		if wanted[name] == nil then
			instance:SetAttribute(name, nil)
		end
	end
end

local function readTags(instance: Instance): Wire
	local tags = instance:GetTags()
	table.sort(tags)
	return { t = "Tags", v = tags }
end

local function writeTags(instance: Instance, tags: { string })
	local wanted = {}
	for _, tag in tags do
		wanted[tag] = true
		instance:AddTag(tag)
	end
	for _, tag in instance:GetTags() do
		if not wanted[tag] then
			instance:RemoveTag(tag)
		end
	end
end

--[[
	Returns every synced property of instance. A property that can't be read or
	represented is left out.
]]
function Properties.readAll(instance: Instance, refOf: RefOf): { [string]: Wire }
	local out: { [string]: Wire } = {}

	for _, name in Properties.names(instance.ClassName) do
		local ok, value = pcall(function()
			return (instance :: any)[name]
		end)
		if ok then
			local wire = Values.encode(value, refOf)
			if wire then
				out[name] = wire
			end
		end
	end

	if instance:IsA("LuaSourceContainer") then
		local source = Values.encode(readSource(instance), refOf)
		if source then
			out[Properties.SOURCE] = source
		end
	end
	out[Properties.ATTRIBUTES] = readAttributes(instance, refOf)
	out[Properties.TAGS] = readTags(instance)

	return out
end

-- Returns one property's wire value, or nil if it can't be read or represented.
function Properties.read(instance: Instance, name: string, refOf: RefOf): Wire?
	if name == Properties.SOURCE then
		if instance:IsA("LuaSourceContainer") then
			return Values.encode(readSource(instance), refOf)
		end
		return nil
	elseif name == Properties.ATTRIBUTES then
		return readAttributes(instance, refOf)
	elseif name == Properties.TAGS then
		return readTags(instance)
	end

	local ok, value = pcall(function()
		return (instance :: any)[name]
	end)
	if not ok then
		return nil
	end
	return Values.encode(value, refOf)
end

-- Sets one property. Returns false and a reason if it couldn't be set.
function Properties.write(instance: Instance, name: string, wire: Wire, resolve: Resolve): (boolean, string?)
	local ok, err = pcall(function()
		if name == Properties.SOURCE then
			writeSource(instance, wire.v :: any)
		elseif name == Properties.ATTRIBUTES then
			writeAttributes(instance, wire.v or {}, resolve)
		elseif name == Properties.TAGS then
			writeTags(instance, wire.v or {})
		else
			local decoded, value = Values.decode(wire, resolve)
			if not decoded then
				error(`can't decode {wire.t} value`, 0)
			end
			(instance :: any)[name] = value
		end
	end)
	if ok then
		return true, nil
	end
	return false, tostring(err)
end

local function noRef(_: Instance): string?
	return nil
end

-- Fingerprints of a new instance's properties, or nothing if it can't be created.
local function defaultsOf(className: string): { [string]: string }
	local cached = defaultsByClass[className]
	if cached then
		return cached
	end

	local defaults = {}
	local ok, instance = pcall(Instance.new, className)
	if ok then
		for name, wire in Properties.readAll(instance, noRef) do
			defaults[name] = Values.fingerprint(wire)
		end
		instance:Destroy()
	end

	defaultsByClass[className] = defaults
	return defaults
end

--[[
	Returns the entries of props worth sending for a new instance: those that
	differ from a new instance of its class. Source is always kept, since a script
	has to exist as a file even when empty.
]]
function Properties.nonDefault(className: string, props: { [string]: Wire }): { [string]: Wire }
	local defaults = defaultsOf(className)
	local out: { [string]: Wire } = {}
	for name, wire in props do
		if name == Properties.SOURCE or defaults[name] ~= Values.fingerprint(wire) then
			out[name] = wire
		end
	end
	return out
end

--[[
	same string for an instance and a wire instance w/ the same props, for telling
	same-named siblings apart. ignores values at the class default, since a file may
	list them and an instance doesn't, and refs, since a file doesn't have them
]]
function Properties.signature(className: string, props: { [string]: Wire }): string
	local withoutRefs: { [string]: Wire } = {}
	for name, wire in props do
		if wire.t ~= "Ref" then
			withoutRefs[name] = wire
		end
	end

	local parts = {}
	for name, wire in Properties.nonDefault(className, withoutRefs) do
		table.insert(parts, name .. "=" .. Values.fingerprint(wire))
	end
	table.sort(parts)
	return table.concat(parts, "\0")
end

return Properties
