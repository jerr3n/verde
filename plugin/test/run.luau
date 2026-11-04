--[[
	Tests the plugin's logic against a fake Studio. Run from plugin/ with:

		lune run test/run

	This can't catch wrong assumptions about real Studio behavior. README.md lists
	what still has to be checked in Studio itself.
]]

local process = require("@lune/process")
local roblox = require("@lune/roblox")
local serde = require("@lune/serde")

local FakeStudio = require("./fake_studio")

local Vector3 = roblox.Vector3

local passed, failed = 0, 0

local function test(name: string, body: () -> ())
	local ok, err = xpcall(body, function(message)
		return debug.traceback(tostring(message), 2)
	end)
	if ok then
		passed += 1
	else
		failed += 1
		print(`FAIL {name}\n{err}\n`)
	end
end

local function eq(actual: any, expected: any, label: string?)
	if actual ~= expected then
		error(`{label or "value"}: got {serde.encode("json", actual)}, want {serde.encode("json", expected)}`, 2)
	end
end

local function check(condition: any, label: string)
	if not condition then
		error(label, 2)
	end
end

local function str(text: string)
	return { t = "String", v = text }
end

-- A world with a Sync whose outgoing patches are collected in `sent`.
local function setup()
	local world = FakeStudio.new()
	local sent = {}
	local sync = world.modules.Sync.new(function(patch)
		table.insert(sent, patch)
	end)
	return world, sync, sent
end

-- A snapshot with ReplicatedStorage as its only sync mount.
local function snapshot(instances: { any }, extra: { [string]: any }?)
	local all = {
		{ id = "game", name = "game", class = "DataModel" },
		{ id = "rs", parent = "game", name = "ReplicatedStorage", class = "ReplicatedStorage" },
	}
	for _, instance in instances do
		table.insert(all, instance)
	end
	local out: { [string]: any } = {
		root = "game",
		mounts = {
			{ id = "game", path = {}, sync = false },
			{ id = "rs", path = { "ReplicatedStorage" }, sync = true },
		},
		instances = all,
	}
	for key, value in extra or {} do
		out[key] = value
	end
	return out
end

local function module(id: string, name: string, source: string, parent: string?)
	return { id = id, parent = parent or "rs", name = name, class = "ModuleScript", props = { Source = str(source) } }
end

-- Lets every queued event fire, then sends whatever changed.
local function settle(world, sync)
	world.pump()
	sync:flush(os.clock() + 10)
end

local function connect(world, sync, instances: { any }, side: string?)
	local plan = sync:plan(snapshot(instances))
	sync:commit(plan, side or "disk")
	settle(world, sync)
	return plan
end

--------------------------------------------------------------------------------
-- Connecting
--------------------------------------------------------------------------------

test("disk: creates what's missing and sends nothing back", function()
	local world, sync, sent = setup()
	connect(world, sync, {
		{ id = "lib", parent = "rs", name = "lib", class = "Folder" },
		module("util", "Util", "return 1", "lib"),
	})

	local rs = world.service("ReplicatedStorage")
	local util = rs:FindFirstChild("lib"):FindFirstChild("Util")
	eq(util.Source, "return 1", "Source")
	eq(#sent, 0, "patches sent")
	eq(world.openRecordings, 0, "open recordings")
end)

test("reconnecting doesn't duplicate instances", function()
	local world, sync = setup()
	local instances = { module("util", "Util", "return 1") }
	connect(world, sync, instances)
	sync:stop()

	local world2Sync = world.modules.Sync.new(function() end)
	world2Sync:commit(world2Sync:plan(snapshot(instances)), "disk")

	eq(#world.service("ReplicatedStorage"):GetChildren(), 1, "children")
end)

test("disk: overwrites a differing Source and leaves other properties alone", function()
	local world, sync, sent = setup()
	local rs = world.service("ReplicatedStorage")
	local existing = world.new("Script", "Main", rs, { Source = "old", Disabled = true })

	connect(
		world,
		sync,
		{ { id = "m", parent = "rs", name = "Main", class = "Script", props = { Source = str("new") } } }
	)

	eq(existing.Source, "new", "Source")
	eq(existing.Disabled, true, "Disabled")
	eq(#sent, 0, "patches sent")
end)

test("disk: Studio-only instances are kept, warned about and not synced", function()
	local world, sync, sent = setup()
	local rs = world.service("ReplicatedStorage")
	local extra = world.new("ModuleScript", "StudioOnly", rs)

	connect(world, sync, {})
	check(extra.Parent == rs, "should still exist")
	check(string.find(table.concat(world.warnings, "\n"), "StudioOnly", 1, true), "should warn")

	extra.Source = "edited"
	settle(world, sync)
	eq(#sent, 0, "patches sent")
end)

test("studio: reports all three kinds of difference", function()
	local world, sync, sent = setup()
	local rs = world.service("ReplicatedStorage")
	world.new("ModuleScript", "Shared", rs, { Source = "studio" })
	world.new("ModuleScript", "New", rs, { Source = "new" })

	connect(world, sync, {
		module("shared", "Shared", "disk"),
		module("gone", "Gone", ""),
	}, "studio")

	eq(#sent, 1, "patches sent")
	local patch = sent[1]
	eq(patch.removed[1], "gone", "removed")
	eq(patch.updated[1].id, "shared", "updated id")
	eq(patch.updated[1].props.Source.v, "studio", "updated Source")
	eq(patch.added[1].name, "New", "added")
	eq(patch.added[1].parent, "rs", "added parent")
	eq(patch.added[1].props.Source.v, "new", "added Source")
	eq(rs:FindFirstChild("Shared").Source, "studio", "Studio's copy is untouched")
	check(rs:FindFirstChild("Gone") == nil, "shouldn't create disk-only instances")
end)

test("siblings with the same name match one to one", function()
	local world, sync = setup()
	local rs = world.service("ReplicatedStorage")
	local a = world.new("Part", "Part", rs)
	local b = world.new("Part", "Part", rs)

	connect(world, sync, {
		{ id = "p1", parent = "rs", name = "Part", class = "Part" },
		{ id = "p2", parent = "rs", name = "Part", class = "Part" },
		{ id = "p3", parent = "rs", name = "Part", class = "Part" },
	})

	eq(#rs:GetChildren(), 3, "children")
	check(sync.refs.p1 == a and sync.refs.p2 == b, "should bind in order")
end)

test("a class mismatch isn't a match", function()
	local world, sync = setup()
	local rs = world.service("ReplicatedStorage")
	local script = world.new("Script", "Thing", rs)

	connect(world, sync, { module("thing", "Thing", "") })

	eq(#rs:GetChildren(), 2, "children")
	check(sync.refs.thing ~= script, "shouldn't bind to a different class")
end)

test("only sync mounts are watched, not the instances on the way to them", function()
	local world = FakeStudio.new()
	local sent = {}
	local sync = world.modules.Sync.new(function(patch)
		table.insert(sent, patch)
	end)
	local player = world.service("StarterPlayer")
	local target = world.new("StarterPlayerScripts", nil, player)
	local other = world.new("StarterCharacterScripts", nil, player)

	sync:commit(
		sync:plan({
			root = "game",
			mounts = {
				{ id = "game", path = {}, sync = false },
				{ id = "sp", path = { "StarterPlayer" }, sync = false },
				{ id = "sps", path = { "StarterPlayer", "StarterPlayerScripts" }, sync = true },
			},
			instances = {
				{ id = "game", name = "game", class = "DataModel" },
				{ id = "sp", parent = "game", name = "StarterPlayer", class = "StarterPlayer" },
				{ id = "sps", parent = "sp", name = "StarterPlayerScripts", class = "StarterPlayerScripts" },
			},
		}),
		"studio"
	)
	world.new("LocalScript", "Outside", other)
	world.new("LocalScript", "Inside", target)
	settle(world, sync)

	eq(#sent, 1, "patches sent")
	eq(#sent[1].added, 1, "added")
	eq(sent[1].added[1].name, "Inside", "added name")
end)

test("a missing mount is reported rather than fatal", function()
	local world, sync = setup()
	local plan = sync:plan({
		root = "game",
		mounts = { { id = "x", path = { "ReplicatedStorage", "Nope" }, sync = true } },
		instances = {},
	})
	eq(plan.missingMounts[1], "ReplicatedStorage/Nope", "missing mount")
	check(world ~= nil, "unused")
end)

--------------------------------------------------------------------------------
-- Studio → server
--------------------------------------------------------------------------------

test("edits, renames and moves become updates", function()
	local world, sync, sent = setup()
	connect(world, sync, {
		{ id = "lib", parent = "rs", name = "lib", class = "Folder" },
		module("util", "Util", "return 1"),
	})
	local rs = world.service("ReplicatedStorage")
	local util = rs:FindFirstChild("Util")

	util.Source = "return 2"
	settle(world, sync)
	eq(sent[1].updated[1].id, "util", "id")
	eq(sent[1].updated[1].props.Source.v, "return 2", "Source")
	eq(sent[1].updated[1].name, nil, "unchanged name")

	util.Name = "Utility"
	settle(world, sync)
	eq(sent[2].updated[1].name, "Utility", "name")
	eq(sent[2].updated[1].props, nil, "unchanged props")

	util.Parent = rs:FindFirstChild("lib")
	settle(world, sync)
	eq(sent[3].updated[1].parent, "lib", "parent")
	eq(sent[3].removed, nil, "a move isn't a removal")
	eq(sent[3].added, nil, "a move isn't an addition")
end)

test("nothing is sent when nothing really changed", function()
	local world, sync, sent = setup()
	connect(world, sync, { module("util", "Util", "same") })
	local util = world.service("ReplicatedStorage"):FindFirstChild("Util")

	util.Source = "other"
	util.Source = "same"
	settle(world, sync)
	eq(#sent, 0, "patches sent")
end)

test("a new subtree is sent parents first, with only non-default properties", function()
	local world, sync, sent = setup()
	connect(world, sync, {})
	local rs = world.service("ReplicatedStorage")

	local model = world.new("Model", "Lamp")
	world.new("Part", "Bulb", model, { Anchored = true })
	world.new("Script", "Glow", model, { Source = "print(1)" })
	world.new("Script", "Empty", model)
	model.Parent = rs
	settle(world, sync)

	eq(#sent, 1, "patches sent")
	local added = sent[1].added
	eq(#added, 4, "added")
	eq(added[1].name, "Lamp", "parent first")
	eq(added[1].parent, "rs", "top parent")
	eq(added[2].parent, added[1].id, "child parent")
	eq(string.lower(added[1].id), added[1].id, "lowercase ref")

	local bulb = added[2]
	eq(bulb.props.Anchored.v, true, "changed property")
	eq(bulb.props.Size, nil, "default property")
	eq(added[4].props.Source.v, "", "an empty script still has Source")
	eq(added[1].props, nil, "no empty props table")

	-- Tracked now, so a later change is an update rather than another add.
	model:FindFirstChild("Bulb").Anchored = false
	settle(world, sync)
	eq(sent[2].added, nil, "added again")
	eq(sent[2].updated[1].id, bulb.id, "updated")
end)

test("deleting removes the whole subtree", function()
	local world, sync, sent = setup()
	connect(world, sync, {
		{ id = "lib", parent = "rs", name = "lib", class = "Folder" },
		module("util", "Util", "", "lib"),
	})
	local lib = world.service("ReplicatedStorage"):FindFirstChild("lib")

	lib:Destroy()
	settle(world, sync)

	local removed = sent[1].removed
	table.sort(removed)
	eq(serde.encode("json", removed), '["lib","util"]', "removed")
	eq(sync.refs.lib, nil, "forgot lib")
	eq(sync.refs.util, nil, "forgot util")
end)

test("undoing a delete brings the instance back as new", function()
	local world, sync, sent = setup()
	connect(world, sync, { module("util", "Util", "x") })
	local rs = world.service("ReplicatedStorage")
	local util = rs:FindFirstChild("Util")

	util.Parent = nil
	settle(world, sync)
	eq(sent[1].removed[1], "util", "removed")

	util.Parent = rs
	settle(world, sync)
	eq(sent[2].added[1].name, "Util", "added")
	eq(sent[2].added[1].props.Source.v, "x", "Source")
	check(sent[2].added[1].id ~= "util", "gets a new Ref")
end)

test("moving out of the synced area is a removal", function()
	local world, sync, sent = setup()
	connect(world, sync, { module("util", "Util", "") })
	local util = world.service("ReplicatedStorage"):FindFirstChild("Util")

	util.Parent = world.service("Workspace")
	settle(world, sync)
	eq(sent[1].removed[1], "util", "removed")

	util.Source = "edited out here"
	settle(world, sync)
	eq(#sent, 1, "no more patches")
end)

test("a tracked instance moved into a brand new parent works in one patch", function()
	local world, sync, sent = setup()
	connect(world, sync, { module("util", "Util", "") })
	local rs = world.service("ReplicatedStorage")
	local util = rs:FindFirstChild("Util")

	local folder = world.new("Folder", "New", rs)
	util.Parent = folder
	settle(world, sync)

	eq(#sent, 1, "patches sent")
	eq(sent[1].added[1].name, "New", "added")
	eq(sent[1].updated[1].id, "util", "updated")
	eq(sent[1].updated[1].parent, sent[1].added[1].id, "new parent")
	eq(sent[1].removed, nil, "removed")
end)

test("ignored instances are never sent", function()
	local world, sync, sent = setup()
	sync:commit(
		sync:plan(snapshot({}, {
			ignore = { classes = { "ObjectValue" }, instances = { "ReplicatedStorage/Secret" } },
		})),
		"studio"
	)
	local rs = world.service("ReplicatedStorage")
	world.new("Camera", "Cam", rs)
	world.new("ObjectValue", "ByClass", rs)
	local secret = world.new("Folder", "Secret", rs)
	world.new("Script", "InsideSecret", secret)
	local hidden = world.new("Folder", "Hidden", rs, { Archivable = false })
	world.new("Script", "InsideHidden", hidden)
	world.new("Folder", "Normal", rs)
	settle(world, sync)

	eq(#sent, 1, "patches sent")
	eq(#sent[1].added, 1, "added")
	eq(sent[1].added[1].name, "Normal", "added name")
end)

test("renaming into an ignored path removes it", function()
	local world, sync, sent = setup()
	sync:commit(
		sync:plan(snapshot({ module("util", "Util", "") }, { ignore = { instances = { "ReplicatedStorage/Secret" } } })),
		"disk"
	)
	settle(world, sync)

	world.service("ReplicatedStorage"):FindFirstChild("Util").Name = "Secret"
	settle(world, sync)
	eq(sent[1].removed[1], "util", "removed")
end)

test("a closed script's Source comes from the property, not the editor", function()
	local world, sync, sent = setup()
	connect(world, sync, { module("util", "Util", "kept") })
	local util = world.service("ReplicatedStorage"):FindFirstChild("Util")

	-- If this were trusted for closed scripts, it would wipe the file.
	world.editorSourceForClosed = ""
	util.Name = "Renamed"
	settle(world, sync)
	eq(sent[1].updated[1].props, nil, "Source shouldn't look changed")
end)

test("typing waits for a pause, and reads the editor's text", function()
	local world, sync, sent = setup()
	connect(world, sync, { module("util", "Util", "start") })
	local util = world.service("ReplicatedStorage"):FindFirstChild("Util")
	world.openDocument(util)

	world.type(util, "typing")
	world.pump()
	sync:flush(os.clock())
	eq(#sent, 0, "sent while typing")

	sync:flush(os.clock() + 10)
	eq(sent[1].updated[1].props.Source.v, "typing", "Source")
	eq(util.Source, "start", "Source property hasn't caught up")
end)

test("attributes and tags sync", function()
	local world, sync, sent = setup()
	connect(world, sync, { { id = "p", parent = "rs", name = "P", class = "Part" } })
	local part = world.service("ReplicatedStorage"):FindFirstChild("P")

	part:SetAttribute("Health", 5)
	settle(world, sync)
	eq(
		serde.encode("json", sent[1].updated[1].props["$attributes"]),
		'{"t":"Attributes","v":[["Health",{"t":"Number","v":5}]]}',
		"attributes"
	)

	-- Roblox has no event for tag changes, so they go out with the next change.
	part:AddTag("Glowing")
	part.Anchored = true
	settle(world, sync)
	eq(sent[2].updated[1].props["$tags"].v[1], "Glowing", "tag")

	part:SetAttribute("Health", nil)
	settle(world, sync)
	eq(#sent[3].updated[1].props["$attributes"].v, 0, "attribute cleared")
end)

test("properties reflection rules out aren't sent", function()
	local world = FakeStudio.new()
	local names = world.modules.Properties.names("Part")
	check(table.find(names, "Anchored"), "should include Anchored")
	check(not table.find(names, "Position"), "not saved with the place")
	check(not table.find(names, "HiddenThing"), "hidden")
	check(not table.find(names, "OldThing"), "deprecated")
end)

test("Ref properties inside a new subtree resolve; ones outside are left out", function()
	local world, sync, sent = setup()
	connect(world, sync, {})
	local rs = world.service("ReplicatedStorage")
	local outside = world.new("Part", "Outside", world.service("Workspace"))

	local model = world.new("Model", "M")
	local part = world.new("Part", "P", model)
	model.PrimaryPart = part
	world.new("ObjectValue", "Pointer", model, { Value = outside })
	model.Parent = rs
	settle(world, sync)

	local added = sent[1].added
	eq(added[1].props.PrimaryPart.t, "Ref", "type")
	eq(added[1].props.PrimaryPart.v, added[2].id, "points at the part")
	eq(added[3].props, nil, "a Ref outside the sync is left out")
end)

--------------------------------------------------------------------------------
-- Server → Studio
--------------------------------------------------------------------------------

test("applying a patch doesn't echo", function()
	local world, sync, sent = setup()
	connect(world, sync, {
		{ id = "lib", parent = "rs", name = "lib", class = "Folder" },
		module("util", "Util", "one"),
		module("gone", "Gone", ""),
	})
	local rs = world.service("ReplicatedStorage")

	sync:applyPatch({
		added = {
			{ id = "m", parent = "lib", name = "M", class = "Model" },
			{ id = "p", parent = "m", name = "P", class = "Part", props = { Anchored = { t = "Bool", v = true } } },
		},
		updated = {
			{ id = "util", name = "Renamed", parent = "lib", props = { Source = str("two") } },
		},
		removed = { "gone" },
	})
	settle(world, sync)

	local lib = rs:FindFirstChild("lib")
	eq(lib:FindFirstChild("M"):FindFirstChild("P").Anchored, true, "added")
	eq(lib:FindFirstChild("Renamed").Source, "two", "updated")
	check(rs:FindFirstChild("Gone") == nil, "removed")
	eq(#sent, 0, "patches sent")
	eq(world.openRecordings, 0, "open recordings")
end)

test("a value the engine rounds doesn't echo", function()
	local world, sync, sent = setup()
	connect(world, sync, { { id = "p", parent = "rs", name = "P", class = "Part" } })
	local part = world.service("ReplicatedStorage"):FindFirstChild("P")

	sync:applyPatch({ updated = { { id = "p", props = { Transparency = { t = "Number", v = 0.1 } } } } })
	check(part.Transparency ~= 0.1, "the fake should round")
	settle(world, sync)
	eq(#sent, 0, "patches sent")
end)

test("a snapshot value the engine rounds doesn't echo later", function()
	local world, sync, sent = setup()
	connect(world, sync, {
		{ id = "p", parent = "rs", name = "P", class = "Part", props = { Transparency = { t = "Number", v = 0.1 } } },
	})
	local part = world.service("ReplicatedStorage"):FindFirstChild("P")
	part.Name = "P2"
	settle(world, sync)
	eq(#sent, 1, "patches sent")
	eq(sent[1].updated[1].props, nil, "only the rename")
end)

test("a local edit after a remote one is sent", function()
	local world, sync, sent = setup()
	connect(world, sync, { module("util", "Util", "one") })
	local util = world.service("ReplicatedStorage"):FindFirstChild("Util")

	sync:applyPatch({ updated = { { id = "util", props = { Source = str("two") } } } })
	settle(world, sync)
	util.Source = "three"
	settle(world, sync)
	eq(#sent, 1, "patches sent")
	eq(sent[1].updated[1].props.Source.v, "three", "Source")
end)

test("an open script is updated through its document", function()
	local world, sync, sent = setup()
	connect(world, sync, { module("util", "Util", "one") })
	local util = world.service("ReplicatedStorage"):FindFirstChild("Util")
	local document = world.openDocument(util)

	sync:applyPatch({ updated = { { id = "util", props = { Source = str("two") } } } })
	settle(world, sync)

	eq(document.text, "two", "editor text")
	eq(world.sourceUpdates, 1, "UpdateSourceAsync calls")
	eq(#sent, 0, "patches sent")
end)

test("removed instances can be undone, and don't echo", function()
	local world, sync, sent = setup()
	connect(world, sync, { module("util", "Util", "") })
	local rs = world.service("ReplicatedStorage")
	local util = rs:FindFirstChild("Util")

	sync:applyPatch({ removed = { "util", "unknown-ref" } })
	settle(world, sync)
	eq(#sent, 0, "patches sent")

	util.Parent = rs -- would throw if the plugin had destroyed it
	settle(world, sync)
	eq(sent[1].added[1].name, "Util", "comes back as new")
end)

test("Ref properties in a patch can point at instances in the same patch", function()
	local world, sync, sent = setup()
	connect(world, sync, {})
	sync:applyPatch({
		added = {
			{ id = "m", parent = "rs", name = "M", class = "Model", props = { PrimaryPart = { t = "Ref", v = "p" } } },
			{ id = "p", parent = "m", name = "P", class = "Part" },
		},
	})
	settle(world, sync)
	local model = world.service("ReplicatedStorage"):FindFirstChild("M")
	check(model.PrimaryPart == model:FindFirstChild("P"), "PrimaryPart")
	eq(#sent, 0, "patches sent")
end)

test("bad instructions warn and don't stop the rest", function()
	local world, sync = setup()
	connect(world, sync, { { id = "p", parent = "rs", name = "P", class = "Part" } })
	local rs = world.service("ReplicatedStorage")

	sync:applyPatch({
		added = {
			{ id = "x", parent = "rs", name = "Bad", class = "NotAClass" },
			{ id = "y", parent = "x", name = "OrphanOfBad", class = "Folder" },
			{ id = "z", parent = "nowhere", name = "NoParent", class = "Folder" },
			{ id = "good", parent = "rs", name = "Good", class = "Folder" },
		},
		updated = {
			{ id = "missing", name = "Nope" },
			{
				id = "p",
				props = {
					Nonsense = { t = "Bool", v = true },
					Anchored = { t = "Bool", v = true },
				},
			},
		},
	})

	check(rs:FindFirstChild("Good") ~= nil, "good instance")
	eq(rs:FindFirstChild("P").Anchored, true, "good property")
	eq(#rs:GetChildren(), 2, "children")
	check(#world.warnings >= 4, "should warn about each problem")
	eq(world.openRecordings, 0, "open recordings")
end)

test("an added instance adopts an untracked match instead of duplicating", function()
	local world, sync = setup()
	local rs = world.service("ReplicatedStorage")
	local existing = world.new("ModuleScript", "Util", rs, { Source = "studio" })
	connect(world, sync, {})

	sync:applyPatch({ added = { module("util", "Util", "disk") } })

	eq(#rs:GetChildren(), 1, "children")
	eq(existing.Source, "disk", "Source")
	check(sync.refs.util == existing, "bound")
end)

test("stop leaves instances in place and stops sending", function()
	local world, sync, sent = setup()
	connect(world, sync, { module("util", "Util", "x") })
	local util = world.service("ReplicatedStorage"):FindFirstChild("Util")

	sync:stop()
	util.Source = "y"
	settle(world, sync)
	eq(util.Source, "y", "still there")
	eq(#sent, 0, "patches sent")
end)

--------------------------------------------------------------------------------
-- Values
--------------------------------------------------------------------------------

test("every value type survives a JSON round trip", function()
	local world = FakeStudio.new()
	local Values = world.modules.Values
	local keypoints = { roblox.NumberSequenceKeypoint.new(0, 1, 0), roblox.NumberSequenceKeypoint.new(1, 2, 0) }
	local colors = {
		roblox.ColorSequenceKeypoint.new(0, roblox.Color3.new(1, 0, 0)),
		roblox.ColorSequenceKeypoint.new(1, roblox.Color3.new(0, 1, 0)),
	}

	local values = {
		"text",
		true,
		1.5,
		roblox.Enum.Material.Neon,
		roblox.Vector2.new(1, 2),
		Vector3.new(1, 2, 3),
		roblox.CFrame.new(1, 2, 3) * roblox.CFrame.Angles(0.5, 0, 0),
		roblox.Color3.new(0.25, 0.5, 0.75),
		roblox.BrickColor.new(21),
		roblox.UDim.new(0.5, 10),
		roblox.UDim2.new(0.5, 10, 0.25, 20),
		roblox.Rect.new(1, 2, 3, 4),
		roblox.NumberRange.new(1, 2),
		roblox.NumberSequence.new(keypoints),
		roblox.ColorSequence.new(colors),
		roblox.PhysicalProperties.new(1, 0.5, 0.25, 2, 3),
	}

	local function noRefs(_)
		return nil
	end
	for _, value in values do
		local wire = Values.encode(value, noRefs)
		check(wire, `couldn't encode a {typeof(value)}`)
		local decodedWire = serde.decode("json", serde.encode("json", wire))
		local ok, decoded = Values.decode(decodedWire, noRefs)
		check(ok, `couldn't decode a {typeof(value)}: {decoded}`)
		eq(Values.fingerprint(Values.encode(decoded, noRefs)), Values.fingerprint(wire), typeof(value))
		eq(typeof(decoded), typeof(value), "type")
	end
end)

test("values JSON can't hold are left out instead of failing the message", function()
	local world = FakeStudio.new()
	local Values = world.modules.Values
	local function noRefs(_)
		return nil
	end
	eq(Values.encode(0 / 0, noRefs), nil, "NaN")
	eq(Values.encode(math.huge, noRefs), nil, "infinity")
	eq(Values.encode(Vector3.new(0 / 0, 0, 0), noRefs), nil, "NaN inside a Vector3")
	eq(Values.encode("\255\254", noRefs), nil, "invalid UTF-8")
	eq(Values.encode(newproxy(false), noRefs), nil, "unknown type")
end)

test("bad wire values fail cleanly", function()
	local world = FakeStudio.new()
	local Values = world.modules.Values
	local function noRefs(_)
		return nil
	end
	check(not Values.decode({ t = "Mystery", v = 1 }, noRefs), "unknown type")
	check(not Values.decode({ t = "Enum", v = { "Material", "NotAMaterial" } }, noRefs), "unknown enum item")
	check(not Values.decode({ t = "Ref", v = "unknown" }, noRefs), "unknown Ref")
	local ok, value = Values.decode({ t = "Nil" }, noRefs)
	check(ok and value == nil, "Nil")
end)

--------------------------------------------------------------------------------
-- Connection
--------------------------------------------------------------------------------

-- A connection with recording callbacks.
local function connectionSetup()
	local world = FakeStudio.new()
	local events = { opened = 0, messages = {} :: { string }, closed = {} :: { any } }
	local connection = world.modules.Connection.new("ws://test", {
		onOpen = function()
			events.opened += 1
		end,
		onMessage = function(message)
			table.insert(events.messages, message)
		end,
		onClosed = function(reason)
			table.insert(events.closed, reason or "no reason")
		end,
	})
	return world, connection, events
end

test("a connection opens a WebSocket and passes messages both ways", function()
	local world, connection, events = connectionSetup()
	local client = world.webStreamClient
	eq(client.kind, "WebSocket", "client type")
	eq(client.url, "ws://test", "url")

	client.Opened:Fire(101, "")
	client.MessageReceived:Fire("from server")
	world.pump()
	eq(events.opened, 1, "opened")
	eq(events.messages[1], "from server", "message")

	check(connection:send("from plugin"), "send should succeed")
	eq(client.sent[1], "from plugin", "sent")
end)

test("an error reports one close with a reason and frees the client", function()
	local world, _, events = connectionSetup()
	local client = world.webStreamClient
	client.Error:Fire(500, "refused")
	client.Closed:Fire()
	world.pump()
	eq(#events.closed, 1, "closes reported")
	check(string.find(events.closed[1], "refused", 1, true), "reason")
	eq(client.closeCount, 1, "client closed")
end)

test("closing locally frees the client and silences callbacks", function()
	local world, connection, events = connectionSetup()
	local client = world.webStreamClient
	connection:close()
	client.MessageReceived:Fire("late")
	client.Closed:Fire()
	world.pump()
	eq(client.closeCount, 1, "client closed")
	eq(#events.messages, 0, "messages")
	eq(#events.closed, 0, "closes reported")
	check(not connection:send("x"), "send should fail")
end)

test("failing to create the client is reported as a close", function()
	local world = FakeStudio.new()
	world.webStreamError = "not allowed"
	local closed = {}
	world.modules.Connection.new("ws://test", {
		onOpen = function() end,
		onMessage = function() end,
		onClosed = function(reason)
			table.insert(closed, reason)
		end,
	})
	require("@lune/task").wait()
	eq(closed[1], "not allowed", "reason")
end)

test("a failed send closes the connection", function()
	local world, connection, events = connectionSetup()
	world.webStreamSendError = "broken"
	check(not connection:send("x"), "send should fail")
	eq(events.closed[1], "broken", "reason")
end)

--------------------------------------------------------------------------------
-- Session
--------------------------------------------------------------------------------

-- A session with a fake connection. `outbox` collects decoded messages.
local function sessionSetup()
	local world = FakeStudio.new()
	local outbox = {}
	local callbacks: any = nil
	local statuses = {}
	local choices = {}
	local closedByPlugin = false

	local session = world.modules.Session.new({
		onStatus = function(status, detail)
			table.insert(statuses, { status = status, detail = detail })
		end,
		onChoice = function(summary)
			table.insert(choices, summary)
		end,
	}, function(_, given)
		callbacks = given
		return {
			send = function(_, message)
				table.insert(outbox, serde.decode("json", message))
				return true
			end,
			close = function()
				closedByPlugin = true
			end,
		}
	end)

	local function receive(messageType: string, data: any)
		callbacks.onMessage(serde.encode("json", { type = messageType, seq = 1, data = data }))
	end

	return {
		world = world,
		session = session,
		outbox = outbox,
		statuses = statuses,
		choices = choices,
		receive = receive,
		open = function()
			session:connect("ws://test")
			callbacks.onOpen()
		end,
		serverClosed = function(reason: string?)
			callbacks.onClosed(reason)
		end,
		wasClosedByPlugin = function()
			return closedByPlugin
		end,
		lastStatus = function()
			return statuses[#statuses]
		end,
	}
end

test("connecting says hello", function()
	local t = sessionSetup()
	t.open()
	eq(t.outbox[1].type, "hello", "type")
	eq(t.outbox[1].seq, 1, "seq")
	eq(t.outbox[1].data.protocolVersion, 1, "version")
	eq(t.outbox[1].data.placeId, 42, "placeId")
	eq(t.lastStatus().status, "waiting", "status")
end)

test("a snapshot connects, then patches apply and are acknowledged", function()
	local t = sessionSetup()
	t.open()
	t.receive("snapshot", snapshot({ module("util", "Util", "one") }))
	t.session:tick(os.clock())
	eq(t.lastStatus().status, "connected", "status")

	t.receive("patch", { updated = { { id = "util", props = { Source = str("two") } } } })
	t.session:tick(os.clock())
	local util = t.world.service("ReplicatedStorage"):FindFirstChild("Util")
	eq(util.Source, "two", "Source")
	eq(t.outbox[2].type, "ack", "ack")

	util.Source = "three"
	t.world.pump()
	t.session:tick(os.clock() + 10)
	eq(t.outbox[3].type, "patch", "outgoing patch")
	eq(t.outbox[3].seq, 3, "seq")
end)

test("an error before the snapshot disconnects and shows the message", function()
	local t = sessionSetup()
	t.open()
	t.receive("error", { message = "place not allowed" })
	t.session:tick(os.clock())
	eq(t.lastStatus().status, "disconnected", "status")
	check(string.find(t.lastStatus().detail, "place not allowed", 1, true), "detail")
	check(t.wasClosedByPlugin(), "should close")
end)

test("an error after connecting only warns", function()
	local t = sessionSetup()
	t.open()
	t.receive("snapshot", snapshot({}))
	t.session:tick(os.clock())
	t.receive("error", { message = "couldn't write file" })
	t.session:tick(os.clock())
	eq(t.lastStatus().status, "connected", "status")
	check(string.find(table.concat(t.world.warnings, "\n"), "couldn't write file", 1, true), "warning")
end)

test("a snapshot without mounts fails clearly", function()
	local t = sessionSetup()
	t.open()
	t.receive("snapshot", { root = "game", instances = {} })
	t.session:tick(os.clock())
	eq(t.lastStatus().status, "disconnected", "status")
	check(string.find(t.lastStatus().detail, "PROTOCOL", 1, true), "should point at the docs")
end)

test("no reply is reported, such as when the server just echoes", function()
	local t = sessionSetup()
	t.open()
	t.receive("hello", { protocolVersion = 1 })
	t.session:tick(os.clock())
	eq(t.lastStatus().detail, nil, "too early")
	t.session:tick(os.clock() + 60)
	check(string.find(t.lastStatus().detail, "snapshot", 1, true), "detail")
end)

test("interactive asks when the sides differ, holding messages until answered", function()
	local t = sessionSetup()
	t.open()
	local rs = t.world.service("ReplicatedStorage")
	t.world.new("ModuleScript", "Studio", rs, { Source = "s" })
	t.receive("snapshot", snapshot({ module("disk", "Disk", "d") }, { onConnect = "interactive" }))
	t.receive("patch", { updated = { { id = "disk", props = { Source = str("d2") } } } })
	t.session:tick(os.clock())

	eq(t.lastStatus().status, "choosing", "status")
	eq(t.choices[1].diskOnly, 1, "diskOnly")
	eq(t.choices[1].studioOnly, 1, "studioOnly")
	check(rs:FindFirstChild("Disk") == nil, "nothing happens before the answer")

	t.session:choose("disk")
	t.session:tick(os.clock())
	eq(t.lastStatus().status, "connected", "status")
	eq(rs:FindFirstChild("Disk").Source, "d2", "the held patch applied afterwards")
end)

test("interactive doesn't ask when nothing differs", function()
	local t = sessionSetup()
	t.open()
	t.world.new("ModuleScript", "Same", t.world.service("ReplicatedStorage"), { Source = "x" })
	t.receive("snapshot", snapshot({ module("same", "Same", "x") }, { onConnect = "interactive" }))
	t.session:tick(os.clock())
	eq(t.lastStatus().status, "connected", "status")
	eq(#t.choices, 0, "choices")
end)

test("studio asks before deleting anything on disk", function()
	local t = sessionSetup()
	t.open()
	t.receive("snapshot", snapshot({ module("disk", "Disk", "") }, { onConnect = "studio" }))
	t.session:tick(os.clock())
	eq(t.lastStatus().status, "choosing", "status")
	check(t.choices[1].wouldDeleteFiles, "wouldDeleteFiles")
	eq(#t.outbox, 1, "only the hello so far")

	t.session:choose("studio")
	t.session:tick(os.clock())
	eq(t.outbox[2].data.removed[1], "disk", "removed")
end)

test("studio doesn't ask when nothing on disk would go", function()
	local t = sessionSetup()
	t.open()
	t.world.new("ModuleScript", "New", t.world.service("ReplicatedStorage"))
	t.receive("snapshot", snapshot({}, { onConnect = "studio" }))
	t.session:tick(os.clock())
	eq(t.lastStatus().status, "connected", "status")
	eq(t.outbox[2].data.added[1].name, "New", "added")
end)

test("messages received just before a close still apply", function()
	local t = sessionSetup()
	t.open()
	t.receive("snapshot", snapshot({ module("util", "Util", "") }))
	t.serverClosed("gone")
	t.session:tick(os.clock())

	check(t.world.service("ReplicatedStorage"):FindFirstChild("Util") ~= nil, "applied")
	eq(t.lastStatus().status, "disconnected", "status")
	eq(t.lastStatus().detail, "gone", "detail")
end)

test("disconnecting stops syncing", function()
	local t = sessionSetup()
	t.open()
	t.receive("snapshot", snapshot({ module("util", "Util", "") }))
	t.session:tick(os.clock())
	t.session:disconnect()

	local count = #t.outbox
	t.world.service("ReplicatedStorage"):FindFirstChild("Util").Source = "x"
	t.world.pump()
	t.session:tick(os.clock() + 10)
	eq(#t.outbox, count, "no more messages")
	check(t.wasClosedByPlugin(), "should close")
end)

test("a broken message doesn't stop the session", function()
	local t = sessionSetup()
	t.open()
	t.receive("snapshot", snapshot({}))
	t.session:tick(os.clock())
	t.receive("patch", { added = "not a list" })
	t.session:tick(os.clock())
	t.receive("patch", { added = { module("ok", "Ok", "") } })
	t.session:tick(os.clock())
	eq(t.lastStatus().status, "connected", "status")
	check(t.world.service("ReplicatedStorage"):FindFirstChild("Ok") ~= nil, "later patch applied")
	eq(t.world.openRecordings, 0, "open recordings")
end)

print(`{passed} passed, {failed} failed`)
process.exit(if failed > 0 then 1 else 0)
