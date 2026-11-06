--[[
	Keeps the instances inside the sync mounts in step with the server.

	Events never decide what changed. They only mark an instance dirty. Every
	tick, flush compares each dirty instance against its shadow, a record of the
	last state both sides agree on, and sends the difference.

	That one rule handles the awkward cases:
	  - Echoes: applying a server change updates the shadow first, so the events it
	    causes find no difference.
	  - Event timing: it doesn't matter whether signals are deferred.
	  - Moves: there's no need to tell a move from a remove and an add.
	  - Undo: an undo is just another difference.
]]

local ChangeHistoryService = game:GetService("ChangeHistoryService")
local HttpService = game:GetService("HttpService")
local ScriptEditorService = game:GetService("ScriptEditorService")

local Properties = require(script.Parent.Properties)
local Values = require(script.Parent.Values)

type Wire = Values.Wire

export type WireInstance = {
	id: string,
	parent: string?,
	name: string,
	class: string,
	props: { [string]: Wire }?,
}

export type Update = {
	id: string,
	name: string?,
	parent: string?,
	props: { [string]: Wire }?,
}

export type Patch = {
	added: { WireInstance }?,
	updated: { Update }?,
	removed: { string }?,
}

export type Snapshot = {
	root: string,
	onConnect: string?,
	ignore: { instances: { string }?, classes: { string }? }?,
	mounts: { { id: string, path: { string }, sync: boolean } },
	instances: { WireInstance },
}

export type Plan = {
	matched: { { instance: Instance, wire: WireInstance } },
	differ: { { instance: Instance, name: string, wire: Wire } },
	diskOnly: { WireInstance },
	studioOnly: { Instance },
	missingMounts: { string },
}

type Shadow = {
	name: string,
	parent: string?,
	props: { [string]: string },
}

-- Wait this long after the last keystroke before sending a script's source.
local TYPING_DELAY = 0.5

local ALWAYS_IGNORED_CLASSES = { Camera = true, Terrain = true }

local Sync = {}
Sync.__index = Sync

function Sync.new(sendPatch: (Patch) -> (), onProblem: ((message: string) -> ())?)
	return setmetatable({
		sendPatch = sendPatch,
		onProblem = onProblem,
		refs = {} :: { [string]: Instance },
		ids = {} :: { [Instance]: string },
		shadows = {} :: { [Instance]: Shadow },
		connections = {} :: { [Instance]: { RBXScriptConnection } },
		mounts = {} :: { [Instance]: boolean },
		globalConnections = {} :: { RBXScriptConnection },
		dirty = {} :: { [Instance]: boolean },
		lastTyped = {} :: { [Instance]: number },
		ignoredClasses = {} :: { [string]: boolean },
		ignoredPaths = {} :: { [string]: boolean },
	}, Sync)
end

local function pathOf(instance: Instance): string
	local names = {}
	local current: Instance? = instance
	while current and current ~= game do
		table.insert(names, 1, current.Name)
		current = current.Parent
	end
	return table.concat(names, "/")
end

local function findMount(path: { string }): Instance?
	local current: Instance = game
	for depth, name in path do
		local child: Instance?
		if depth == 1 then
			-- Services may not be loaded until something asks for them.
			local ok, service = pcall(function()
				return game:GetService(name)
			end)
			child = if ok then service else nil
		end
		child = child or current:FindFirstChild(name)
		if child == nil then
			return nil
		end
		current = child
	end
	return current
end

local function newRef(): string
	return string.lower(HttpService:GenerateGUID(false))
end

-- Something couldn't be applied. Logged, and reported so the panel can show it.
function Sync:_problem(message: string)
	warn(`[Verde] {message}`)
	if self.onProblem then
		self.onProblem(message)
	end
end

function Sync:_refOf(instance: Instance): string?
	return self.ids[instance]
end

function Sync:_resolve(id: string): Instance?
	return self.refs[id]
end

function Sync:_isIgnored(instance: Instance): boolean
	return not instance.Archivable
		or ALWAYS_IGNORED_CLASSES[instance.ClassName] == true
		or self.ignoredClasses[instance.ClassName] == true
		or self.ignoredPaths[pathOf(instance)] == true
end

-- Whether instance is inside a mount with nothing ignored on the way up.
function Sync:_isEligible(instance: Instance): boolean
	local current: Instance? = instance
	while current do
		if self.mounts[current] then
			return true
		elseif self:_isIgnored(current) then
			return false
		end
		current = current.Parent
	end
	return false
end

function Sync:_bind(instance: Instance, id: string)
	self.refs[id] = instance
	self.ids[instance] = id
end

function Sync:_readProps(instance: Instance): { [string]: Wire }
	return Properties.readAll(instance, function(target)
		return self:_refOf(target)
	end)
end

function Sync:_write(instance: Instance, name: string, wire: Wire)
	local ok, err = Properties.write(instance, name, wire, function(id)
		return self:_resolve(id)
	end)
	if not ok then
		self:_problem(`couldn't set {pathOf(instance)}.{name}: {err}`)
	end
end

-- Records an instance's current state as agreed, and starts watching it.
function Sync:_track(instance: Instance, props: { [string]: Wire }?)
	local fingerprints = {}
	for name, wire in props or self:_readProps(instance) do
		fingerprints[name] = Values.fingerprint(wire)
	end
	self.shadows[instance] = {
		name = instance.Name,
		parent = self.ids[instance.Parent :: any],
		props = fingerprints,
	}

	if self.connections[instance] == nil then
		local function mark()
			self.dirty[instance] = true
		end
		self.connections[instance] = {
			instance.Changed:Connect(mark),
			instance.AttributeChanged:Connect(mark),
		}
	end
end

-- Stops tracking instance and its descendants, listing their Refs in removed.
function Sync:_forget(instance: Instance, removed: { string }?)
	local function forgetOne(target: Instance)
		local id = self.ids[target]
		if id == nil or self.mounts[target] then
			return
		end
		if removed then
			table.insert(removed, id)
		end
		self.refs[id] = nil
		self.ids[target] = nil
		self.shadows[target] = nil
		self.lastTyped[target] = nil
		for _, connection in self.connections[target] or {} do
			connection:Disconnect()
		end
		self.connections[target] = nil
	end

	forgetOne(instance)
	for _, descendant in instance:GetDescendants() do
		forgetOne(descendant)
	end
end

--[[
	Creates instances from wire form, parents before children. Children are parented
	last so that Ref properties can resolve and nothing appears half-built.
]]
function Sync:_create(wires: { WireInstance })
	local entries = {}

	for _, wire in wires do
		local parent = self.refs[wire.parent :: string]
		if self.refs[wire.id] then
			self:_problem(`ignoring duplicate added instance {wire.name}`)
		elseif parent == nil then
			self:_problem(`skipping {wire.name}: its parent isn't synced`)
		else
			-- An untracked match can exist if disk mode left it alone earlier.
			local existing: Instance?
			for _, child in parent:GetChildren() do
				if child.Name == wire.name and child.ClassName == wire.class and self.ids[child] == nil then
					existing = child
					break
				end
			end

			local instance = existing
			if instance == nil then
				local ok, created = pcall(Instance.new, wire.class)
				if ok then
					instance = created
				else
					self:_problem(`skipping {wire.name}: can't create a {wire.class}`)
				end
			end

			if instance then
				instance.Name = wire.name
				self:_bind(instance, wire.id)
				table.insert(entries, { instance = instance, wire = wire, isNew = existing == nil })
			end
		end
	end

	for _, entry in entries do
		for name, value in entry.wire.props or {} do
			self:_write(entry.instance, name, value)
		end
	end
	for _, entry in entries do
		if entry.isNew then
			entry.instance.Parent = self.refs[entry.wire.parent :: string]
		end
	end
	for _, entry in entries do
		self:_track(entry.instance)
	end
end

-- Assigns Refs to instance and its descendants and appends them to added.
function Sync:_adopt(instance: Instance, added: { WireInstance })
	local list = {}
	local function visit(target: Instance)
		if self.ids[target] or self:_isIgnored(target) then
			return
		end
		self:_bind(target, newRef())
		table.insert(list, target)
		for _, child in target:GetChildren() do
			visit(child)
		end
	end
	visit(instance)

	-- Everything has a Ref by now, so Ref properties inside the subtree encode.
	for _, target in list do
		local props = self:_readProps(target)
		local sent = Properties.nonDefault(target.ClassName, props)
		table.insert(added, {
			id = self.ids[target],
			parent = self.ids[target.Parent :: any],
			name = target.Name,
			class = target.ClassName,
			props = if next(sent) then sent else nil,
		})
		self:_track(target, props)
	end
end

-- Returns what changed on a tracked instance since its shadow, or nil.
function Sync:_diff(instance: Instance): Update?
	local shadow = self.shadows[instance]
	if shadow == nil then
		return nil
	end

	local update: Update = { id = self.ids[instance] }
	local changed = false

	if instance.Name ~= shadow.name then
		shadow.name = instance.Name
		update.name = instance.Name
		changed = true
	end

	local parentId = self.ids[instance.Parent :: any]
	if parentId ~= shadow.parent then
		shadow.parent = parentId
		update.parent = parentId
		changed = true
	end

	local props: { [string]: Wire } = {}
	for name, wire in self:_readProps(instance) do
		local fingerprint = Values.fingerprint(wire)
		if shadow.props[name] ~= fingerprint then
			shadow.props[name] = fingerprint
			props[name] = wire
			changed = true
		end
	end
	if next(props) then
		update.props = props
	end

	return if changed then update else nil
end

local function record(name: string, body: () -> ())
	local recording = ChangeHistoryService:TryBeginRecording(name)
	local ok, err = pcall(body)
	if recording then
		ChangeHistoryService:FinishRecording(recording, Enum.FinishRecordingOperation.Commit)
	end
	if not ok then
		error(err, 0)
	end
end

--[[
	Compares a snapshot with Studio without changing any instances. The result
	says how the two differ; pass it to commit once a side has been chosen.
]]
function Sync:plan(snapshot: Snapshot): Plan
	local ignore = snapshot.ignore or {}
	for _, class in ignore.classes or {} do
		self.ignoredClasses[class] = true
	end
	for _, path in ignore.instances or {} do
		self.ignoredPaths[path] = true
	end

	local mountIds = {}
	for _, mount in snapshot.mounts do
		mountIds[mount.id] = true
	end

	local childrenOf: { [string]: { WireInstance } } = {}
	for _, wire in snapshot.instances do
		if wire.parent and not mountIds[wire.id] then
			local siblings = childrenOf[wire.parent]
			if siblings == nil then
				siblings = {}
				childrenOf[wire.parent] = siblings
			end
			table.insert(siblings, wire)
		end
	end

	local plan: Plan = { matched = {}, differ = {}, diskOnly = {}, studioOnly = {}, missingMounts = {} }

	local function addDiskOnly(wire: WireInstance)
		table.insert(plan.diskOnly, wire)
		for _, child in (childrenOf[wire.id] or {}) :: { WireInstance } do
			addDiskOnly(child)
		end
	end

	local function match(instance: Instance, id: string)
		local unclaimed = instance:GetChildren()
		for _, wire in (childrenOf[id] or {}) :: { WireInstance } do
			local found: Instance?
			for index, child in unclaimed do
				if child.Name == wire.name and child.ClassName == wire.class then
					found = child
					table.remove(unclaimed, index)
					break
				end
			end
			if found then
				self:_bind(found, wire.id)
				table.insert(plan.matched, { instance = found, wire = wire })
				match(found, wire.id)
			else
				addDiskOnly(wire)
			end
		end
		for _, child in unclaimed do
			if not self:_isIgnored(child) then
				table.insert(plan.studioOnly, child)
			end
		end
	end

	for _, mount in snapshot.mounts do
		if mount.sync then
			local instance = findMount(mount.path)
			if instance then
				self.mounts[instance] = true
				self:_bind(instance, mount.id)
				match(instance, mount.id)
			else
				table.insert(plan.missingMounts, table.concat(mount.path, "/"))
			end
		end
	end

	-- Compared only after matching, so Ref properties can see every binding.
	for _, pair in plan.matched do
		for name, wire in pair.wire.props or {} do
			local current = Properties.read(pair.instance, name, function(target)
				return self:_refOf(target)
			end)
			if current == nil or Values.fingerprint(current) ~= Values.fingerprint(wire) then
				table.insert(plan.differ, { instance = pair.instance, name = name, wire = wire })
			end
		end
	end

	return plan
end

-- Applies a plan with disk or Studio as the winner, then starts watching.
function Sync:commit(plan: Plan, side: string)
	local patch = { updated = {}, removed = {}, added = {} }

	record("Verde: sync with project", function()
		if side == "disk" then
			self:_create(plan.diskOnly)
			for _, entry in plan.differ do
				self:_write(entry.instance, entry.name, entry.wire)
			end
		else
			for _, wire in plan.diskOnly do
				table.insert(patch.removed, wire.id)
			end
			for _, entry in plan.differ do
				local current = Properties.read(entry.instance, entry.name, function(target)
					return self:_refOf(target)
				end)
				if current then
					table.insert(patch.updated, { id = self.ids[entry.instance], props = { [entry.name] = current } })
				end
			end
		end
	end)

	for _, pair in plan.matched do
		self:_track(pair.instance)
	end

	if side == "disk" then
		for index, instance in plan.studioOnly do
			if index > 10 then
				warn(`[Verde] ...and {#plan.studioOnly - 10} more`)
				break
			end
			warn(`[Verde] {pathOf(instance)} exists only in Studio, so it isn't being synced`)
		end
	else
		for _, instance in plan.studioOnly do
			self:_adopt(instance, patch.added)
		end
	end

	self:_watch()

	if #patch.added > 0 or #patch.updated > 0 or #patch.removed > 0 then
		self.sendPatch({
			added = if #patch.added > 0 then patch.added else nil,
			updated = if #patch.updated > 0 then patch.updated else nil,
			removed = if #patch.removed > 0 then patch.removed else nil,
		})
	end
end

function Sync:_watch()
	local function mark(instance: Instance)
		self.dirty[instance] = true
	end
	for mount in self.mounts do
		table.insert(self.globalConnections, mount.DescendantAdded:Connect(mark))
		table.insert(self.globalConnections, mount.DescendantRemoving:Connect(mark))
	end
	table.insert(
		self.globalConnections,
		ScriptEditorService.TextDocumentDidChange:Connect(function(document)
			if document:IsCommandBar() then
				return
			end
			local target = document:GetScript()
			if target and self.ids[target] then
				self.lastTyped[target] = os.clock()
				self.dirty[target] = true
			end
		end)
	)
end

-- Applies a patch from the server. The events this causes find nothing to send.
function Sync:applyPatch(patch: Patch)
	record("Verde: apply changes", function()
		self:_create(patch.added or {})

		for _, update in (patch.updated or {}) :: { Update } do
			local instance = self.refs[update.id]
			local shadow = self.shadows[instance :: any]
			if instance == nil or shadow == nil then
				self:_problem(`ignoring an update for unknown instance {update.id}`)
			else
				if update.name then
					instance.Name = update.name
				end
				if update.parent then
					local target = self.refs[update.parent]
					if target then
						instance.Parent = target
					else
						self:_problem(`can't move {instance.Name}: the new parent isn't synced`)
					end
				end
				for name, wire in update.props or {} do
					self:_write(instance, name, wire)
					local current = Properties.read(instance, name, function(target)
						return self:_refOf(target)
					end)
					-- Read back, since the engine may round what it was given.
					shadow.props[name] = if current then Values.fingerprint(current) else nil
				end
				shadow.name = instance.Name
				shadow.parent = self.ids[instance.Parent :: any]
			end
		end

		for _, id in (patch.removed or {}) :: { string } do
			local instance = self.refs[id]
			if instance and not self.mounts[instance] then
				self:_forget(instance)
				-- Not Destroy, which would stop this from being undoable.
				instance.Parent = nil
			end
		end
	end)
end

-- Sends whatever changed since the last flush.
function Sync:flush(now: number)
	if next(self.dirty) == nil then
		return
	end

	local ready = {}
	for instance in self.dirty do
		local typed = self.lastTyped[instance]
		if typed == nil or now - typed >= TYPING_DELAY then
			table.insert(ready, instance)
		end
	end
	for _, instance in ready do
		self.dirty[instance] = nil
		self.lastTyped[instance] = nil
	end

	local added: { WireInstance } = {}
	local updated: { Update } = {}
	local removed: { string } = {}

	-- New instances first, so a tracked instance moved into one can refer to it.
	-- A new child seen before its new parent is picked up by the parent's visit.
	for _, instance in ready do
		local parent = instance.Parent
		if self.ids[instance] == nil and parent and self.ids[parent] and self:_isEligible(instance) then
			self:_adopt(instance, added)
		end
	end

	for _, instance in ready do
		if self.ids[instance] and not self.mounts[instance] then
			if self:_isEligible(instance) then
				local update = self:_diff(instance)
				if update then
					table.insert(updated, update)
				end
			else
				self:_forget(instance, removed)
			end
		end
	end

	if #added > 0 or #updated > 0 or #removed > 0 then
		self.sendPatch({
			added = if #added > 0 then added else nil,
			updated = if #updated > 0 then updated else nil,
			removed = if #removed > 0 then removed else nil,
		})
	end
end

-- Stops syncing. Instances stay as they are.
function Sync:stop()
	for _, connection in self.globalConnections do
		connection:Disconnect()
	end
	for _, connections in self.connections do
		for _, connection in connections do
			connection:Disconnect()
		end
	end
	table.clear(self.globalConnections)
	table.clear(self.connections)
	table.clear(self.dirty)
end

return Sync
