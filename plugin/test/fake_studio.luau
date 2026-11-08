-- SPDX-License-Identifier: MPL-2.0

--[[
	A small fake of Studio for testing the plugin's modules in Lune. Each call to
	new() builds a separate world and loads fresh copies of the modules into it.

	Data types are real, from @lune/roblox. Instances, events and services are fake.
	Events are queued until pump(), like Roblox's deferred signals, which is the
	timing most likely to break echo handling.
]]

local fs = require("@lune/fs")
local luau = require("@lune/luau")
local roblox = require("@lune/roblox")
local serde = require("@lune/serde")
local task = require("@lune/task")

local Enum = roblox.Enum
local Vector3 = roblox.Vector3
local Color3 = roblox.Color3

local FakeStudio = {}

local MODULE_NAMES = { "Values", "Properties", "Sync", "Connection", "Notifications", "Session" }

-- Read up front, because reading a file can't yield inside a metamethod.
local sources: { [string]: string } = {}
for _, name in MODULE_NAMES do
	sources[name] = fs.readFile(`src/Verde/{name}.luau`)
end

local SCRIPT_DEFAULTS = { Source = "", Disabled = false }

-- A property set to this kind of value starts as nil.
local NIL = newproxy(false)

local CLASSES: { [string]: any } = {
	DataModel = { creatable = false, props = {} },
	Workspace = { creatable = false, props = {} },
	ReplicatedStorage = { creatable = false, props = {} },
	ServerScriptService = { creatable = false, props = {} },
	StarterPlayer = { creatable = false, props = {} },
	StarterPlayerScripts = { creatable = false, props = {} },
	StarterCharacterScripts = { creatable = false, props = {} },
	Terrain = { creatable = false, props = {} },
	Camera = { creatable = true, props = {} },
	Folder = { creatable = true, props = {} },
	Model = { creatable = true, props = { PrimaryPart = NIL } },
	ObjectValue = { creatable = true, props = { Value = NIL } },
	Part = {
		creatable = true,
		props = {
			Anchored = false,
			Transparency = 0,
			Size = Vector3.new(4, 1.2, 2),
			Material = Enum.Material.Plastic,
			Color = Color3.new(0.5, 0.5, 0.5),
		},
	},
	Script = { creatable = true, isScript = true, props = SCRIPT_DEFAULTS },
	LocalScript = { creatable = true, isScript = true, props = SCRIPT_DEFAULTS },
	ModuleScript = { creatable = true, isScript = true, props = { Source = "" } },
}

-- Properties ReflectionService reports that the plugin must not sync.
local UNSYNCED_REFLECTION = {
	{ Name = "Position", Serialized = false, Display = { Category = "Data" } },
	{ Name = "HiddenThing", Serialized = true, Display = nil },
	{ Name = "OldThing", Serialized = true, Display = { Category = "Data", DeprecationMessage = "Don't" } },
	{ Name = "archivable", Serialized = true, Display = { Category = "Data" } },
}

-- Properties real Studio saves under another name. Reflection reports that name as the
-- serialized one, and the real one as not serialized. Reading or writing the saved name
-- fails here, so a plugin that syncs it fails the way it would in Studio.
local SAVED_AS: { [string]: { name: string, hidden: boolean } } = {
	Size = { name = "size", hidden = false },
	Color = { name = "Color3uint8", hidden = true },
}

-- Stored in single precision, like many real properties, so it reads back rounded.
local ROUNDED = { Transparency = true }

function FakeStudio.new()
	local world = {
		warnings = {} :: { string },
		queue = {} :: { any },
		documents = {} :: { [any]: any },
		openRecordings = 0,
		recordings = 0,
		sourceUpdates = 0,
	}

	local isInstance = {}

	local function newSignal()
		local signal = { listeners = {} :: { any } }
		function signal:Connect(fn)
			local connection = { connected = true }
			function connection:Disconnect()
				connection.connected = false
			end
			table.insert(signal.listeners, { fn = fn, connection = connection })
			return connection
		end
		function signal:Fire(...)
			local args = table.pack(...)
			-- Listeners are captured now: one that connects later misses this event.
			local listeners = table.clone(signal.listeners)
			table.insert(world.queue, function()
				for _, listener in listeners do
					if listener.connection.connected then
						listener.fn(table.unpack(args, 1, args.n))
					end
				end
			end)
		end
		return signal
	end

	local function makeInstance(className: string)
		local class = CLASSES[className]
		local state = {
			className = className,
			name = className,
			parent = nil :: any,
			children = {} :: { any },
			archivable = true,
			destroyed = false,
			props = {} :: { [string]: any },
			attributes = {} :: { [string]: any },
			tags = {} :: { [string]: boolean },
			changed = newSignal(),
			attributeChanged = newSignal(),
			descendantAdded = newSignal(),
			descendantRemoving = newSignal(),
		}
		for name, value in class.props do
			if value ~= NIL then
				state.props[name] = value
			end
		end

		local instance = newproxy(true)
		isInstance[instance] = state
		local meta = getmetatable(instance)

		local methods = {}
		function methods:GetChildren()
			return table.clone(state.children)
		end
		function methods:GetDescendants()
			local out = {}
			local function visit(item)
				for _, child in isInstance[item].children do
					table.insert(out, child)
					visit(child)
				end
			end
			visit(instance)
			return out
		end
		function methods:FindFirstChild(name)
			for _, child in state.children do
				if child.Name == name then
					return child
				end
			end
			return nil
		end
		function methods:IsA(name)
			return name == className or (name == "LuaSourceContainer" and class.isScript == true)
		end
		function methods:IsDescendantOf(ancestor)
			local current = state.parent
			while current do
				if current == ancestor then
					return true
				end
				current = current.Parent
			end
			return false
		end
		function methods:Destroy()
			for _, child in table.clone(state.children) do
				child:Destroy()
			end
			instance.Parent = nil
			state.destroyed = true
		end
		function methods:GetAttributes()
			return table.clone(state.attributes)
		end
		function methods:SetAttribute(name, value)
			state.attributes[name] = value
			state.attributeChanged:Fire(name)
		end
		function methods:GetTags()
			local out = {}
			for tag in state.tags do
				table.insert(out, tag)
			end
			return out
		end
		function methods:AddTag(tag)
			state.tags[tag] = true
		end
		function methods:RemoveTag(tag)
			state.tags[tag] = nil
		end

		meta.__tostring = function()
			return state.name
		end
		meta.__index = function(_, key)
			if key == "Name" then
				return state.name
			elseif key == "ClassName" then
				return className
			elseif key == "Parent" then
				return state.parent
			elseif key == "Archivable" then
				return state.archivable
			elseif key == "Changed" then
				return state.changed
			elseif key == "AttributeChanged" then
				return state.attributeChanged
			elseif key == "DescendantAdded" then
				return state.descendantAdded
			elseif key == "DescendantRemoving" then
				return state.descendantRemoving
			elseif methods[key] then
				return methods[key]
			elseif class.props[key] ~= nil then
				return state.props[key]
			end
			error(`{key} is not a valid member of {className}`, 2)
		end
		meta.__newindex = function(_, key, value)
			if key == "Name" then
				if state.name ~= value then
					state.name = value
					state.changed:Fire("Name")
				end
			elseif key == "Archivable" then
				state.archivable = value
			elseif key == "Parent" then
				if state.destroyed then
					error("The Parent property is locked", 2)
				end
				if state.parent == value then
					return
				end
				local subtree = { instance }
				for _, descendant in instance:GetDescendants() do
					table.insert(subtree, descendant)
				end
				local function fireAncestors(start, signalName)
					local current = start
					while current do
						for _, item in subtree do
							isInstance[current][signalName]:Fire(item)
						end
						current = current.Parent
					end
				end
				if state.parent then
					fireAncestors(state.parent, "descendantRemoving")
					local siblings = isInstance[state.parent].children
					table.remove(siblings, table.find(siblings, instance) :: number)
				end
				state.parent = value
				if value then
					table.insert(isInstance[value].children, instance)
					fireAncestors(value, "descendantAdded")
				end
				state.changed:Fire("Parent")
			elseif class.props[key] ~= nil then
				if ROUNDED[key] and type(value) == "number" then
					value = string.unpack("f", string.pack("f", value))
				end
				if state.props[key] ~= value then
					state.props[key] = value
					state.changed:Fire(key)
				end
			else
				error(`{key} is not a valid member of {className}`, 2)
			end
		end

		return instance
	end

	local game = makeInstance("DataModel")
	world.game = game
	isInstance[game].props.PlaceId = 42

	local services: { [string]: any } = {}

	local guidCounter = 0
	services.HttpService = {
		JSONEncode = function(_, value)
			return serde.encode("json", value)
		end,
		JSONDecode = function(_, text)
			return serde.decode("json", text)
		end,
		GenerateGUID = function(_, _)
			guidCounter += 1
			return `GUID-{guidCounter}`
		end,
		CreateWebStreamClient = function(_, kind, options)
			if world.webStreamError then
				error(world.webStreamError, 0)
			end
			local client = {
				kind = kind,
				url = options.Url,
				sent = {} :: { string },
				closeCount = 0,
				Opened = newSignal(),
				MessageReceived = newSignal(),
				Error = newSignal(),
				Closed = newSignal(),
			}
			function client:Send(message)
				if world.webStreamSendError then
					error(world.webStreamSendError, 0)
				end
				table.insert(client.sent, message)
			end
			function client:Close()
				client.closeCount += 1
			end
			world.webStreamClient = client
			return client
		end,
	}
	services.ReflectionService = {
		GetPropertiesOfClass = function(_, className)
			local class = CLASSES[className]
			if class == nil then
				error("unknown class", 0)
			end
			local out = table.clone(UNSYNCED_REFLECTION)
			local display = { Category = "Data" }
			for name in class.props do
				local saved = SAVED_AS[name]
				table.insert(out, { Name = name, Serialized = saved == nil, Display = display })
				if saved then
					table.insert(out, {
						Name = saved.name,
						Serialized = true,
						Display = if saved.hidden then nil else display,
					})
				end
			end
			return out
		end,
	}
	world.textChanged = newSignal()
	services.ScriptEditorService = {
		TextDocumentDidChange = world.textChanged,
		GetEditorSource = function(_, target)
			local document = world.documents[target]
			if document then
				return document.text
			end
			return if world.editorSourceForClosed then world.editorSourceForClosed else target.Source
		end,
		FindScriptDocument = function(_, target)
			return world.documents[target]
		end,
		UpdateSourceAsync = function(_, target, callback)
			task.wait() -- the real one yields
			local document = world.documents[target]
			document.text = callback(document.text)
			target.Source = document.text
			world.sourceUpdates += 1
		end,
	}
	services.ChangeHistoryService = {
		TryBeginRecording = function(_, _)
			world.openRecordings += 1
			world.recordings += 1
			return `recording-{world.recordings}`
		end,
		FinishRecording = function(_, _, _)
			world.openRecordings -= 1
		end,
	}

	local gameMeta = getmetatable(game)
	local baseIndex = gameMeta.__index
	gameMeta.__index = function(self, key)
		if key == "GetService" then
			return function(_, name)
				if services[name] then
					return services[name]
				end
				local found = game:FindFirstChild(name)
				if found then
					return found
				end
				local class = CLASSES[name]
				if class == nil then
					error(`{name} is not a valid service`, 0)
				end
				local service = makeInstance(name)
				service.Parent = game
				return service
			end
		elseif key == "PlaceId" then
			return 42
		end
		return baseIndex(self, key)
	end

	-- Creates an instance the way a person in Studio would.
	function world.new(className: string, name: string?, parent: any?, props: { [string]: any }?)
		local instance = makeInstance(className)
		if name then
			instance.Name = name
		end
		for key, value in props or {} do
			instance[key] = value
		end
		instance.Parent = parent
		return instance
	end

	function world.service(name: string)
		return game:GetService(name)
	end

	-- Delivers queued events, including any that listeners cause.
	function world.pump()
		while #world.queue > 0 do
			table.remove(world.queue, 1)()
		end
	end

	function world.openDocument(target)
		local document = { text = target.Source }
		function document:IsCommandBar()
			return false
		end
		function document:GetScript()
			return target
		end
		world.documents[target] = document
		return document
	end

	-- Types into an open script without changing Source, as the editor does.
	function world.type(target, text: string)
		local document = world.documents[target]
		document.text = text
		world.textChanged:Fire(document, {})
	end

	local fakeInstance = {
		new = function(className: string)
			local class = CLASSES[className]
			if class == nil or not class.creatable then
				error(`Unable to create an Instance of type "{className}"`, 0)
			end
			return makeInstance(className)
		end,
	}

	local environment: { [string]: any } = {
		game = game,
		Instance = fakeInstance,
		-- WebSocket is too new for the enum data Lune ships with.
		Enum = setmetatable({ WebStreamClientType = { WebSocket = "WebSocket" } }, { __index = roblox.Enum }),
		task = task,
		Vector2 = roblox.Vector2,
		Vector3 = roblox.Vector3,
		CFrame = roblox.CFrame,
		Color3 = roblox.Color3,
		BrickColor = roblox.BrickColor,
		UDim = roblox.UDim,
		UDim2 = roblox.UDim2,
		Rect = roblox.Rect,
		NumberRange = roblox.NumberRange,
		NumberSequence = roblox.NumberSequence,
		NumberSequenceKeypoint = roblox.NumberSequenceKeypoint,
		ColorSequence = roblox.ColorSequence,
		ColorSequenceKeypoint = roblox.ColorSequenceKeypoint,
		Font = roblox.Font,
		PhysicalProperties = roblox.PhysicalProperties,
		Content = roblox.Content,
		typeof = function(value)
			return if isInstance[value] then "Instance" else typeof(value)
		end,
		warn = function(...)
			local parts = {}
			for _, part in { ... } do
				table.insert(parts, tostring(part))
			end
			table.insert(world.warnings, table.concat(parts, " "))
		end,
	}

	local loaded: { [string]: any } = {}
	local tree = {}
	local function load(name: string)
		if loaded[name] == nil then
			-- Copied, because Lune replaces the metatable of an environment it's given.
			local moduleEnvironment = table.clone(environment)
			moduleEnvironment.script = { Parent = tree, Name = name }
			loaded[name] = luau.load(sources[name], { debugName = name, environment = moduleEnvironment })()
		end
		return loaded[name]
	end
	for _, name in MODULE_NAMES do
		tree[name] = { moduleName = name }
	end
	environment.require = function(token)
		return load(token.moduleName)
	end

	world.modules = setmetatable({}, {
		__index = function(_, name)
			return load(name)
		end,
	})

	return world
end

return FakeStudio
