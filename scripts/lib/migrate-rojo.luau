-- SPDX-License-Identifier: MPL-2.0

-- Converts a resolved Rojo place to Verde's on-disk format. The command wrapper
-- builds with Rojo first, so Rojo remains responsible for its project semantics.
local fs = require("@lune/fs")
local luau = require("@lune/luau")
local roblox = require("@lune/roblox")
local serde = require("@lune/serde")

-- Use the same value encoder as the Studio plugin.
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
local scriptDir = string.match(debug.info(1, "s"), "^(.*)[/\\][^/\\]+$")
local repo = `{scriptDir}/../..`
local reflected = serde.decode("json", fs.readFile(`{repo}/executable/internal/build/reflection.json`))
local Values = luau.load(fs.readFile(`{repo}/plugin/src/Verde/Values.luau`), {
	debugName = "Values",
	environment = environment,
})()

local Migration = {}
local suffixes = { Script = ".server.luau", LocalScript = ".client.luau", ModuleScript = ".luau" }
local skipped = {
	Name = true,
	Parent = true,
	ClassName = true,
	Source = true,
	Attributes = true,
	AttributesSerialize = true,
	Tags = true,
	UniqueId = true,
	HistoryId = true,
	CurrentCamera = true,
}
-- These stored property names cannot be assigned under those names in Studio.
local realNames = {
	archivable = "Archivable",
	Color3uint8 = "Color",
	DefinesCapabilities = "Sandboxed",
	EmitterSize = "RollOffMinDistance",
	formFactorRaw = "FormFactor",
	heat_xml = "Heat",
	MaterialVariantSerialized = "MaterialVariant",
	opacity_xml = "Opacity",
	Part0Internal = "Part0",
	Part1Internal = "Part1",
	riseVelocity_xml = "RiseVelocity",
	shape = "Shape",
	size = "Size",
	size_xml = "Size",
	xmlRead_MaxDistance_3 = "RollOffMaxDistance",
}

-- Keep this consistent with executable/internal/project/names.go. Every instance
-- also gets an explicit name and id, so same-named siblings stay distinct.
function Migration.encodeName(name: string): string
	assert(name ~= "", "Verde cannot sync an instance with an empty name")
	local encoded = string.gsub(name, ".", function(c)
		local byte = string.byte(c)
		if byte < 32 or byte == 127 or string.find('%/\\:*?"<>|', c, 1, true) then
			return string.format("%%%02X", byte)
		end
		return c
	end)
	encoded = string.gsub(encoded, "^%.", "%%2E")
	encoded = string.gsub(encoded, "%.$", "%%2E")
	encoded = string.gsub(encoded, " $", "%%20")
	encoded = string.gsub(encoded, "%.server$", "%%2Eserver")
	encoded = string.gsub(encoded, "%.client$", "%%2Eclient")
	local stem = string.upper(string.split(encoded, ".")[1])
	if
		encoded == "init"
		or stem == "CON"
		or stem == "PRN"
		or stem == "AUX"
		or stem == "NUL"
		or string.match(stem, "^COM[1-9]$")
		or string.match(stem, "^LPT[1-9]$")
	then
		encoded = string.format("%%%02X", string.byte(encoded, 1)) .. string.sub(encoded, 2)
	end
	return encoded
end

local function sortedKeys(t): { string }
	local names = {}
	for name in t do
		table.insert(names, name)
	end
	table.sort(names)
	return names
end

local function json(value): string
	return serde.encode("json", value)
end

local function read(instance, name)
	local ok, value = pcall(function()
		return instance[name]
	end)
	return ok, value
end

local function propertyNames(className): { string }
	local names = {}
	local class = reflected.classes[className]
	assert(class ~= nil, `Verde does not know class {className}; update Verde and retry`)
	while class do
		for name in class.props or {} do
			name = realNames[name] or name
			if not skipped[name] then
				names[name] = true
			end
		end
		class = reflected.classes[class.super or ""]
	end
	return sortedKeys(names)
end

function Migration.convert(game, project, root: string)
	local warnings = {}
	local count = 0
	local nextID = 0
	local defaults = {}
	local namesCache = {}
	local sync = {}
	local paths = {}

	local function warn(path, message)
		table.insert(warnings, `{path}: {message}`)
	end

	local function propsOf(instance, path)
		local class = instance.ClassName
		if not defaults[class] then
			defaults[class] = roblox.Instance.new(class)
			namesCache[class] = propertyNames(class)
		end
		local props = {}
		for _, name in namesCache[class] do
			local ok, value = read(instance, name)
			local defaultOK, default = read(defaults[class], name)
			if ok and (not defaultOK or value ~= default) then
				if typeof(value) == "Instance" then
					warn(path, `{name} points to another instance; Verde cannot store Ref properties yet`)
				elseif value == nil and defaultOK and typeof(default) == "Instance" then
					warn(path, `{name} is an instance reference; Verde cannot store Ref properties yet`)
				else
					local encoded = Values.encode(value, function()
						return nil
					end)
					if encoded then
						local encodedDefault = if defaultOK
							then Values.encode(default, function()
								return nil
							end)
							else nil
						-- Sequences and other userdata do not compare by value in Lune.
						if not encodedDefault or json(encoded) ~= json(encodedDefault) then
							props[name] = encoded
						end
					elseif not defaultOK or tostring(value) ~= tostring(default) then
						warn(path, `{name} has an unsupported value ({typeof(value)})`)
					end
				end
			end
		end
		-- Lune can store these legacy properties separately from CFrame. Fold
		-- them into the transform, as Studio does when reading the Rojo build.
		if instance:IsA("BasePart") then
			local transform = instance.CFrame
			local changed = false
			local positionOK, position = read(instance, "Position")
			if positionOK then
				transform = roblox.CFrame.new(position) * (transform - transform.Position)
				changed = true
			end
			local rotationOK, rotation = read(instance, "Rotation")
			if rotationOK then
				transform = roblox.CFrame.new(transform.Position)
					* roblox.CFrame.Angles(math.rad(rotation.X), math.rad(rotation.Y), math.rad(rotation.Z))
				changed = true
			end
			local orientationOK, orientation = read(instance, "Orientation")
			if orientationOK then
				transform = roblox.CFrame.new(transform.Position)
					* roblox.CFrame.fromOrientation(
						math.rad(orientation.X),
						math.rad(orientation.Y),
						math.rad(orientation.Z)
					)
				changed = true
			end
			if changed then
				props.CFrame = Values.encode(transform, function()
					return nil
				end)
			end
		end
		local attributes = {}
		for _, name in sortedKeys(instance:GetAttributes()) do
			local value = Values.encode(instance:GetAttribute(name), function()
				return nil
			end)
			if value then
				table.insert(attributes, { name, value })
			else
				warn(path, `attribute {name} has an unsupported value`)
			end
		end
		if #attributes > 0 then
			props["$attributes"] = { t = "Attributes", v = attributes }
		end
		local tags = instance:GetTags()
		if #tags > 0 then
			table.sort(tags)
			props["$tags"] = { t = "Tags", v = tags }
		end
		return props
	end

	local function writeChildren(parent, dir, instancePath)
		-- Reserve all names case-insensitively for Windows and macOS too.
		local used = {}
		for _, instance in parent:GetChildren() do
			local path = `{instancePath}/{instance.Name}`
			if instance.ClassName == "Camera" or instance.ClassName == "Terrain" then
				warn(path, `{instance.ClassName} instances are not synced by Verde; subtree skipped`)
				continue
			end
			nextID += 1
			local id = `rojo-migration-{nextID}`
			local base = Migration.encodeName(instance.Name)
			if used[string.lower(base)] then
				base ..= `~{id}`
			end
			while used[string.lower(base)] do
				base ..= "~"
			end
			used[string.lower(base)] = true
			local childDir = `{dir}/{base}`
			fs.writeDir(`{root}/{childDir}`)
			local props = propsOf(instance, path)
			local file = { id = id, name = instance.Name }
			if next(props) then
				file.props = props
			end
			local suffix = suffixes[instance.ClassName]
			assert(string.match(instance.ClassName, "^[%a_][%w_]*$"), `invalid class {instance.ClassName}`)
			fs.writeFile(
				`{root}/{childDir}/init.{instance.ClassName}.inst.json`,
				serde.encode("json", file, true) .. "\n"
			)
			if suffix then
				assert(utf8.len(instance.Source) ~= nil, `{path}: script source is not valid UTF-8`)
				fs.writeFile(`{root}/{childDir}/init{suffix}`, instance.Source)
			end
			count += 1
			writeChildren(instance, childDir, path)
		end
	end

	local function mount(instance, instancePath, dir)
		assert(not paths[instancePath], `duplicate mount {instancePath}`)
		paths[instancePath] = true
		for _, prop in sortedKeys(propsOf(instance, instancePath)) do
			warn(instancePath, `{prop} belongs to a sync target; Verde only syncs its children`)
		end
		fs.writeDir(`{root}/{dir}`)
		table.insert(sync, { instance = instancePath, path = dir })
		writeChildren(instance, dir, instancePath)
	end

	-- StarterPlayerScripts and StarterCharacterScripts are existing containers,
	-- so mount them directly instead of trying to create them as regular children.
	for _, service in game:GetChildren() do
		local class = roblox.getReflectionDatabase():GetClass(service.ClassName)
		assert(class and table.find(class.Tags, "Service"), `{service.Name}: expected a service at the place root`)
		assert(
			service.Name == service.ClassName and not string.find(service.Name, "/", 1, true),
			`{service.Name}: the root must be a place with services named after their classes`
		)
		if service.ClassName == "StarterPlayer" then
			for _, prop in sortedKeys(propsOf(service, service.Name)) do
				warn(service.Name, `{prop} belongs to a service; Verde only syncs its children`)
			end
			for _, container in service:GetChildren() do
				assert(
					container.Name == container.ClassName
						and (
							container.ClassName == "StarterPlayerScripts"
							or container.ClassName == "StarterCharacterScripts"
						),
					`StarterPlayer/{container.Name}: expected StarterPlayerScripts or StarterCharacterScripts`
				)
				mount(container, `StarterPlayer/{container.Name}`, `src/StarterPlayer/{container.Name}`)
			end
		else
			mount(service, service.Name, `src/{Migration.encodeName(service.Name)}`)
		end
	end
	assert(#sync > 0, "the Rojo place has no sync targets")
	table.sort(sync, function(a, b)
		return a.instance < b.instance
	end)
	-- JSON scalars are valid YAML and keep arbitrary project names unambiguous.
	local lines = {
		"# Migrated from Rojo. See migration-report.txt before connecting Studio.",
		"version: 0",
		`name: {json(project.name or "migrated-game")}`,
		"serve:",
		"  address: 127.0.0.1",
		"  port: 45678",
		`  placeIds: {json(project.servePlaceIds or {})}`,
		"sync:",
		"  instances:",
	}
	for _, entry in sync do
		table.insert(lines, `    - instance: {json(entry.instance)}`)
		table.insert(lines, `      path: {json(entry.path)}`)
	end
	-- Use literal [] rather than an empty Luau table (which serializes as {}).
	for i, line in lines do
		if line == "  placeIds: {}" then
			lines[i] = "  placeIds: []"
		end
	end
	table.insert(
		lines,
		'ignore:\n  paths: ["**/*.tmp", "**/.DS_Store"]\n  instances: ["Workspace/Camera", "Workspace/Terrain"]'
	)
	table.insert(lines, "conflicts:\n  onConnect: interactive")
	fs.writeFile(`{root}/project.yml`, table.concat(lines, "\n") .. "\n")

	local report = {
		`Migrated {count} instances into {#sync} sync targets.`,
		"",
		"This is a resolved copy of the Rojo project. Continue editing src/ here.",
		"Rojo source files are unchanged. rojo-snapshot.rbxlx preserves the built input.",
		"Rojo build output is in rojo-build.log; check it for Rojo warnings.",
		"Source paths were reorganized. Copy and adapt tool configs and non-instance files separately.",
		"Rojo sync rules and ignoreUnknownInstances are not carried over; Verde uses its own sync rules.",
		"Only files known to Rojo were migrated; unsynced Studio content is still in your place.",
		"Service properties and instance references cannot currently be stored by Verde.",
		"Connect the Verde plugin to ws://127.0.0.1:45678/ws and review the initial sync.",
		"",
		`Warnings ({#warnings}):`,
	}
	for _, warning in warnings do
		table.insert(report, `- {warning}`)
	end
	fs.writeFile(`{root}/migration-report.txt`, table.concat(report, "\n") .. "\n")
	return { instances = count, mounts = #sync, warnings = warnings }
end

return Migration
