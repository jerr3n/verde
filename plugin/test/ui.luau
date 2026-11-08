-- SPDX-License-Identifier: MPL-2.0

-- Runs the real UI with Roblox property validation and small event/plugin fakes.
-- Lune does not render layouts; resizing and text wrapping still need Studio QA.
local fs = require("@lune/fs")
local luau = require("@lune/luau")
local process = require("@lune/process")
local roblox = require("@lune/roblox")

local MODULE_FILES = {
	Ui = "src/Verde/Ui/init.luau",
	Theme = "src/Verde/Ui/Theme.luau",
	Kit = "src/Verde/Ui/Kit.luau",
	Toasts = "src/Verde/Ui/Toasts.luau",
	Notifications = "src/Verde/Notifications.luau",
}

-- Read up front, because reading a file can't yield during a require.
local sources = {}
for name, path in MODULE_FILES do
	sources[name] = fs.readFile(path)
end

local function signal()
	local listeners = {}
	return {
		Connect = function(_, callback)
			local connection = { connected = true }
			function connection:Disconnect()
				self.connected = false
			end
			table.insert(listeners, { connection, callback })
			return connection
		end,
		Fire = function(_, ...)
			for _, listener in listeners do
				if listener[1].connected then
					listener[2](...)
				end
			end
		end,
		count = function()
			local total = 0
			for _, listener in listeners do
				if listener[1].connected then
					total += 1
				end
			end
			return total
		end,
	}
end

local function setup(saved: { [string]: any }?)
	local objects, rawObjects, destroyed = {}, {}, {}
	local events = { Activated = true, MouseEnter = true, MouseLeave = true, Focused = true, FocusLost = true }
	local function instance(className)
		local raw = roblox.Instance.new(className)
		local object = newproxy(true)
		local signals, changed = {}, {}
		objects[raw] = object
		rawObjects[object] = raw
		getmetatable(object).__index = function(_, key)
			if events[key] then
				signals[key] = signals[key] or signal()
				return signals[key]
			elseif key == "GetPropertyChangedSignal" then
				return function(_, property)
					changed[property] = changed[property] or signal()
					return changed[property]
				end
			elseif key == "Destroy" then
				return function()
					destroyed[object] = true
					raw:Destroy()
				end
			elseif key == "FindFirstChild" then
				return function(_, name)
					return objects[raw:FindFirstChild(name)]
				end
			elseif key == "GetChildren" then
				return function()
					local children = {}
					for _, child in raw:GetChildren() do
						table.insert(children, objects[child])
					end
					return children
				end
			elseif key == "AbsoluteSize" then
				-- Only the engine's layout can answer this.
				return roblox.Vector2.new(320, 64)
			end
			local value = raw[key]
			return objects[value] or value
		end
		getmetatable(object).__newindex = function(_, key, value)
			-- Lune has property types for widgets but not all of their defaults.
			local _, previous = pcall(function()
				return raw[key]
			end)
			raw[key] = rawObjects[value] or value
			if previous ~= raw[key] and changed[key] then
				changed[key]:Fire()
			end
		end
		return object
	end

	local studio = { ThemeChanged = signal() }
	local light = false
	studio.Theme = {
		GetColor = function(_, guide)
			if guide == roblox.Enum.StudioStyleGuideColor.MainText then
				return if light then roblox.Color3.fromRGB(30, 30, 30) else roblox.Color3.fromRGB(230, 230, 230)
			end
			return if light then roblox.Color3.fromRGB(242, 242, 242) else roblox.Color3.fromRGB(37, 37, 37)
		end,
	}
	local toggle = {
		Click = signal(),
		SetActive = function(self, active)
			self.active = active
		end,
	}
	local toolbarIcon
	local plugin = { saved = table.clone(saved or {}) }
	function plugin:CreateToolbar()
		return {
			CreateButton = function(_, _, _, icon)
				toolbarIcon = icon
				return toggle
			end,
		}
	end
	function plugin:CreateDockWidgetPluginGuiAsync()
		local widget = instance("DockWidgetPluginGui")
		widget.Enabled = true
		return widget
	end
	function plugin:GetSetting(name)
		return self.saved[name]
	end
	function plugin:SetSetting(name, value)
		self.saved[name] = value
	end

	-- Properties change at once, so tests see the end state. Completed waits for
	-- flushTweens, because a real tween can't finish before Play returns.
	local finishing = {}
	local function flushTweens()
		while #finishing > 0 do
			table.remove(finishing, 1):Fire(roblox.Enum.PlaybackState.Completed)
		end
	end
	local tweenService = {
		Create = function(_, object, _, goal)
			local completed = signal()
			return {
				Completed = completed,
				Play = function()
					for key, value in goal do
						object[key] = value
					end
					table.insert(finishing, completed)
				end,
				Cancel = function() end,
			}
		end,
	}

	local coreGui = instance("Folder")
	local staleGui
	if (saved or {}).__staleGui then
		staleGui = instance("ScreenGui")
		staleGui.Name = "VerdeNotifications"
		staleGui.Parent = coreGui
	end
	local coreGuiAvailable = (saved or {}).__noCoreGui == nil
	local environment = {
		game = {
			GetService = function(_, name)
				if name == "TweenService" then
					return tweenService
				elseif name == "CoreGui" then
					if not coreGuiAvailable then
						error("The current identity cannot access CoreGui", 0)
					end
					return coreGui
				end
				error(`unexpected service {name}`, 0)
			end,
		},
		Instance = { new = instance },
		Enum = roblox.Enum,
		Color3 = roblox.Color3,
		Vector2 = roblox.Vector2,
		UDim = roblox.UDim,
		UDim2 = roblox.UDim2,
		TweenInfo = {
			new = function(...)
				return { ... }
			end,
		},
		DockWidgetPluginGuiInfo = {
			new = function(...)
				return { ... }
			end,
		},
		settings = function()
			return { Studio = studio }
		end,
	}

	local root: any = { moduleName = nil }
	root.Notifications = { moduleName = "Notifications", Parent = root }
	local uiToken: any = { moduleName = "Ui", Parent = root }
	root.Ui = uiToken
	for _, name in { "Theme", "Kit", "Toasts" } do
		uiToken[name] = { moduleName = name, Parent = uiToken }
	end

	local loaded = {}
	local function load(token)
		local name = token.moduleName
		if loaded[name] == nil then
			-- Copied, because Lune replaces the metatable of an environment it's given.
			local moduleEnvironment = table.clone(environment)
			moduleEnvironment.script = token
			moduleEnvironment.require = load
			loaded[name] = luau.load(sources[name], { debugName = name, environment = moduleEnvironment })()
		end
		return loaded[name]
	end

	local Ui = load(uiToken)
	local calls = { connects = {}, disconnects = 0, choices = {} }
	local ui
	ui = Ui.new(plugin, {
		onConnect = function(url)
			table.insert(calls.connects, url)
			ui:setStatus("connecting")
		end,
		onDisconnect = function()
			calls.disconnects += 1
			ui:setStatus("disconnected")
		end,
		onChoose = function(side)
			table.insert(calls.choices, side)
		end,
	})

	local function toastFrames()
		flushTweens()
		local gui = coreGui:FindFirstChild("VerdeNotifications")
		if gui == nil then
			return {}
		end
		local frames = {}
		for _, child in gui:FindFirstChild("Holder"):GetChildren() do
			if child.Name == "Toast" and not destroyed[child] then
				table.insert(frames, child)
			end
		end
		return frames
	end

	return {
		ui = ui,
		plugin = plugin,
		calls = calls,
		toggle = toggle,
		toolbarIcon = toolbarIcon,
		studio = studio,
		coreGui = coreGui,
		staleGui = staleGui,
		destroyed = destroyed,
		toastFrames = toastFrames,
		card = function(frame)
			return frame:FindFirstChild("Card")
		end,
		text = function(frame, name)
			return frame:FindFirstChild("Card"):FindFirstChild("Content"):FindFirstChild(name)
		end,
		setLight = function()
			light = true
			studio.ThemeChanged:Fire()
		end,
	}
end

local passed, failed = 0, 0
local function test(name, body)
	local ok, err = xpcall(body, function(message)
		return debug.traceback(tostring(message), 2)
	end)
	if ok then
		passed += 1
	else
		failed += 1
		print(`FAIL {name}\n{err}`)
	end
end

local function equal(actual, expected)
	assert(actual == expected, `got {tostring(actual)}, expected {tostring(expected)}`)
end

local function info(overrides: { [string]: any }?)
	local base = {
		url = "ws://127.0.0.1:34873/ws",
		mounts = { "ReplicatedStorage", "ServerScriptService" },
		connectedAt = 100,
		fromDisk = 3,
		toDisk = 2,
		lastChangeAt = nil,
	}
	for key, value in overrides or {} do
		base[key] = value
	end
	return base
end

--------------------------------------------------------------------------------
-- Panel
--------------------------------------------------------------------------------

test("the logo is used by the toolbar and the header", function()
	local t = setup()
	assert(string.find(t.toolbarIcon, "107008526393062", 1, true), "toolbar icon")
	equal(t.ui.logo.Image, t.toolbarIcon)
end)

test("the logo and tabs share one bar, above the pages", function()
	local t = setup()
	local bar = t.ui.logo.Parent
	equal(t.ui.tabButtons.Sync.Parent.Parent, bar)
	equal(t.ui.pages.Sync.Position.Y.Offset, bar.Size.Y.Offset)
	equal(bar.Parent:FindFirstChild("Header"), nil)
end)

test("uses a saved address and ignores malformed settings", function()
	equal(setup({ VerdeServerUrl = "wss://example.com/ws" }).ui.urlBox.Text, "wss://example.com/ws")
	equal(setup({ VerdeServerUrl = {} }).ui.urlBox.Text, "ws://127.0.0.1:34873/ws")
end)

test("invalid addresses stay local and clear their error on edit", function()
	local t = setup()
	for _, address in { "", "   ", "http://localhost", "ws://", "ws:///ws", "ws://local host/ws" } do
		t.ui.urlBox.Text = address
		t.ui.connectButton.Activated:Fire()
		assert(t.ui.inputError.Visible)
	end
	equal(#t.calls.connects, 0)
	equal(t.plugin.saved.VerdeServerUrl, nil)
	t.ui.urlBox.Text = "ws://localhost:34873/ws"
	equal(t.ui.inputError.Visible, false)
end)

test("Enter trims and saves the address, and a pending connection can be cancelled", function()
	local t = setup()
	t.ui.urlBox.Text = "  wss://example.com/ws  "
	t.ui.urlBox.FocusLost:Fire(true)
	equal(t.calls.connects[1], "wss://example.com/ws")
	equal(t.plugin.saved.VerdeServerUrl, "wss://example.com/ws")
	equal(t.ui.urlBox.TextEditable, false)
	equal(t.ui.connectButton.Text, "Cancel")
	t.ui.urlBox.FocusLost:Fire(true)
	equal(#t.calls.connects, 1)
	t.ui.connectButton.Activated:Fire()
	equal(t.calls.disconnects, 1)
	equal(t.ui.urlBox.TextEditable, true)
end)

test("status details, help and connected details follow the session", function()
	local t = setup()
	equal(t.ui.statusLabel.Text, "Ready to connect")
	t.ui:setStatus("waiting", "Snapshot timed out")
	equal(t.ui.statusDetail.Text, "Snapshot timed out")
	equal(t.ui.help.Visible, false)
	equal(t.ui.addressSection.Visible, true)
	t.ui:setStatus("connected")
	equal(t.ui.connectButton.Text, "Disconnect")
	equal(t.ui.addressSection.Visible, false)
	equal(t.ui.infoSection.Visible, true)
	t.ui:setStatus("disconnected", "Server went away")
	equal(t.ui.statusLabel.Text, "Connection ended")
	equal(t.ui.statusDetail.Text, "Server went away")
	equal(t.ui.help.Visible, true)
	equal(t.ui.infoSection.Visible, false)
	t.ui:setStatus("disconnected", "Refused", "Couldn't reach the server")
	equal(t.ui.statusLabel.Text, "Couldn't reach the server")
end)

test("tick fills in the live connection details", function()
	local t = setup()
	t.ui:setStatus("connected")
	t.ui:tick(100 + 125, info())
	equal(t.ui.infoRows.server.Text, "ws://127.0.0.1:34873/ws")
	equal(t.ui.infoRows.mounts.Text, "ReplicatedStorage, ServerScriptService")
	equal(t.ui.infoRows.uptime.Text, "for 2m")
	equal(t.ui.infoRows.transfer.Text, "3 from disk, 2 to disk")
	equal(t.ui.infoRows.last.Text, "None yet")
	t.ui:tick(100 + 3700, info({ lastChangeAt = 100 + 3690, mounts = {} }))
	equal(t.ui.infoRows.uptime.Text, "for 1h 1m")
	equal(t.ui.infoRows.last.Text, "10s ago")
	equal(t.ui.infoRows.mounts.Text, "Nothing")
end)

test("the panel's title only ever adds an unread count", function()
	local t = setup()
	equal(t.ui.widget.Title, "Verde")
	for _, status in { "connecting", "waiting", "choosing", "connected" } do
		t.ui:setStatus(status)
		equal(t.ui.widget.Title, "Verde")
	end
	t.ui:notify({ kind = "error", title = "Oops" })
	equal(t.ui.widget.Title, "Verde (1)")
	t.ui:selectTab("Activity")
	equal(t.ui.widget.Title, "Verde")
end)

test("tabs switch pages", function()
	local t = setup()
	equal(t.ui.pages.Sync.Visible, true)
	equal(t.ui.pages.Activity.Visible, false)
	t.ui.tabButtons.Settings.Activated:Fire()
	equal(t.ui.pages.Settings.Visible, true)
	equal(t.ui.pages.Sync.Visible, false)
end)

test("review shows counts and deletion warning, and only accepts one choice", function()
	local t = setup()
	t.ui:setStatus("choosing")
	t.ui:askChoice({ differ = 12, diskOnly = 3, studioOnly = 4, wouldDeleteFiles = true })
	equal(t.ui.countLabels.differ.Text, "12")
	equal(t.ui.countLabels.diskOnly.Text, "3")
	equal(t.ui.countLabels.studioOnly.Text, "4")
	assert(t.ui.choicePanel.Visible and t.ui.deleteWarning.Visible)
	assert(string.find(t.ui.deleteWarning.Text, "3 instances", 1, true))
	t.ui.keepStudio.Activated:Fire()
	t.ui.keepDisk.Activated:Fire()
	equal(#t.calls.choices, 1)
	equal(t.calls.choices[1], "studio")
	equal(t.ui.keepStudio.Active, false)
	t.ui:setStatus("connected")
	equal(t.ui.choicePanel.Visible, false)
	t.ui:setStatus("choosing")
	t.ui:askChoice({ differ = 1, diskOnly = 0, studioOnly = 0, wouldDeleteFiles = false })
	equal(t.ui.deleteWarning.Visible, false)
	equal(t.ui.keepStudio.Active, true)
	t.ui.keepDisk.Activated:Fire()
	equal(t.calls.choices[2], "disk")
end)

test("a choice opens a closed panel on the Sync tab", function()
	local t = setup()
	t.ui.widget.Enabled = false
	t.ui:selectTab("Settings")
	t.ui:setStatus("choosing")
	t.ui:askChoice({ differ = 1, diskOnly = 0, studioOnly = 0, wouldDeleteFiles = false })
	equal(t.ui.widget.Enabled, true)
	equal(t.ui.pages.Sync.Visible, true)
end)

test("themes, panel toggle and unload update existing controls", function()
	local t = setup()
	local before = t.ui.urlBox.TextColor3
	t.setLight()
	assert(t.ui.urlBox.TextColor3 ~= before)
	t.toggle.Click:Fire()
	equal(t.ui.widget.Enabled, false)
	equal(t.toggle.active, false)
	t.ui:destroy()
	assert(t.destroyed[t.ui.widget])
	-- The theme listener must be disconnected before destroyed controls are used.
	equal(t.studio.ThemeChanged.count(), 0)
	t.studio.ThemeChanged:Fire()
end)

--------------------------------------------------------------------------------
-- Activity feed
--------------------------------------------------------------------------------

test("notices appear in the feed, newest first", function()
	local t = setup()
	equal(t.ui.feedEmpty.Visible, true)
	local first = t.ui.notifications:push({ kind = "info", title = "First" })
	t.ui:_renderFeed()
	local second = t.ui.notifications:push({ kind = "info", title = "Second", detail = "More" })
	t.ui:_renderFeed()
	equal(t.ui.feedEmpty.Visible, false)
	local firstRow, secondRow = t.ui.rows[first.id], t.ui.rows[second.id]
	equal(firstRow.title.Text, "First")
	equal(firstRow.detail.Visible, false)
	equal(secondRow.detail.Text, "More")
	assert(secondRow.frame.LayoutOrder < firstRow.frame.LayoutOrder, "newest should sort first")
	assert(string.match(secondRow.time.Text, "^%d%d:%d%d:%d%d$"), "time")
end)

test("a merged notice updates its row instead of adding one", function()
	local t = setup()
	t.ui:notify({ kind = "activity", key = "fromDisk", title = "One", merge = false, routine = true })
	t.ui:notify({ kind = "info", title = "Between" })
	t.ui:notify({ kind = "activity", key = "fromDisk", title = "Two", merge = true, routine = true })
	local entries = t.ui.notifications:all()
	equal(#entries, 2)
	equal(entries[2].title, "Two")
	equal(t.ui.rows[entries[2].id].title.Text, "Two")
	assert(t.ui.rows[entries[2].id].frame.LayoutOrder < t.ui.rows[entries[1].id].frame.LayoutOrder, "moves to the top")
end)

test("only problems count as unread, and viewing the feed clears them", function()
	local t = setup()
	t.ui:notify({ kind = "success", title = "Fine" })
	t.ui:notify({ kind = "activity", title = "Routine" })
	equal(t.ui.badge.Visible, false)
	t.ui:notify({ kind = "error", title = "Bad" })
	t.ui:notify({ kind = "warning", title = "Hmm" })
	equal(t.ui.badge.Visible, true)
	equal(t.ui.badge.Text, "2")
	t.ui:selectTab("Activity")
	equal(t.ui.badge.Visible, false)
	t.ui:notify({ kind = "error", title = "Seen right away" })
	equal(t.ui.badge.Visible, false)
end)

test("a closed panel doesn't count as viewing the feed", function()
	local t = setup()
	t.ui:selectTab("Activity")
	t.ui.widget.Enabled = false
	t.ui:notify({ kind = "error", title = "Bad" })
	equal(t.ui.badge.Visible, true)
	t.ui.widget.Enabled = true
	equal(t.ui.badge.Visible, false)
end)

test("many unread problems cap the badge's text", function()
	local t = setup()
	for _ = 1, 12 do
		t.ui:notify({ kind = "warning", title = "Hmm" })
	end
	equal(t.ui.badge.Text, "9+")
end)

test("clearing empties the feed and destroys its rows", function()
	local t = setup()
	t.ui:notify({ kind = "error", title = "Bad" })
	local _, row = next(t.ui.rows)
	t.ui.clearButton.Activated:Fire()
	equal(next(t.ui.rows), nil)
	assert(t.destroyed[row.frame], "row should be destroyed")
	equal(t.ui.feedEmpty.Visible, true)
	equal(t.ui.badge.Visible, false)
end)

test("the feed keeps a bounded history", function()
	local t = setup()
	for index = 1, 130 do
		t.ui:notify({ kind = "activity", title = `Event {index}`, quiet = true })
	end
	equal(#t.ui.notifications:all(), 100)
	local rows = 0
	for _ in t.ui.rows do
		rows += 1
	end
	equal(rows, 100)
end)

--------------------------------------------------------------------------------
-- Pop-ups
--------------------------------------------------------------------------------

test("a notice pops up with its title, detail and tone", function()
	local t = setup()
	t.ui:notify({ kind = "success", key = "connection", title = "Connected", detail = "All good" })
	local frames = t.toastFrames()
	equal(#frames, 1)
	equal(t.text(frames[1], "Title").Text, "Connected")
	equal(t.text(frames[1], "Detail").Text, "All good")
	equal(t.text(frames[1], "Action").Visible, false)
	equal(t.card(frames[1]).Position, roblox.UDim2.new())
end)

test("a quiet notice is recorded without a pop-up", function()
	local t = setup()
	t.ui:notify({ kind = "activity", key = "toDisk", title = "Sent", quiet = true })
	equal(#t.toastFrames(), 0)
	equal(#t.ui.notifications:all(), 1)
end)

test("pop-ups with the same key replace each other", function()
	local t = setup()
	t.ui:notify({ kind = "warning", key = "connection", title = "Review needed", sticky = true, action = "review" })
	t.ui:notify({ kind = "success", key = "connection", title = "Connected" })
	local frames = t.toastFrames()
	equal(#frames, 1)
	equal(t.text(frames[1], "Title").Text, "Connected")
	equal(t.text(frames[1], "Action").Visible, false)
	t.ui:tick(os.clock() + 100, nil)
	equal(#t.toastFrames(), 0)
end)

test("pop-ups expire on their own, but sticky ones wait", function()
	local t = setup()
	t.ui:notify({ kind = "info", title = "Bye" })
	t.ui:notify({ kind = "error", title = "Stays", sticky = true })
	equal(#t.toastFrames(), 2)
	t.ui:tick(os.clock(), nil)
	equal(#t.toastFrames(), 2)
	t.ui:tick(os.clock() + 1000, nil)
	local frames = t.toastFrames()
	equal(#frames, 1)
	equal(t.text(frames[1], "Title").Text, "Stays")
end)

test("hovering holds a pop-up open, with a short grace period after", function()
	local t = setup()
	t.ui:notify({ kind = "info", title = "Hold" })
	local frame = t.toastFrames()[1]
	t.card(frame).MouseEnter:Fire()
	local later = os.clock() + 1000
	t.ui:tick(later, nil)
	equal(#t.toastFrames(), 1)
	t.card(frame).MouseLeave:Fire()
	t.ui:tick(later + 1, nil)
	equal(#t.toastFrames(), 1)
	t.ui:tick(later + 3, nil)
	equal(#t.toastFrames(), 0)
end)

test("only three show at once, dropping the oldest that isn't sticky", function()
	local t = setup()
	t.ui:notify({ kind = "error", title = "Keep", sticky = true })
	t.ui:notify({ kind = "info", title = "Old" })
	t.ui:notify({ kind = "info", title = "Middle" })
	t.ui:notify({ kind = "info", title = "New" })
	local titles = {}
	for _, frame in t.toastFrames() do
		titles[t.text(frame, "Title").Text] = true
	end
	equal(#t.toastFrames(), 3)
	assert(titles.Keep and titles.Middle and titles.New and not titles.Old, "wrong pop-up dropped")
end)

test("the dismiss button closes a pop-up", function()
	local t = setup()
	t.ui:notify({ kind = "error", title = "Bad", sticky = true })
	local frame = t.toastFrames()[1]
	t.card(frame):FindFirstChild("Dismiss").Activated:Fire()
	equal(#t.toastFrames(), 0)
end)

test("clicking a pop-up opens the panel on the right tab", function()
	local t = setup()
	t.ui.widget.Enabled = false
	t.ui:notify({ kind = "error", title = "Bad" })
	t.card(t.toastFrames()[1]).Activated:Fire()
	equal(t.ui.widget.Enabled, true)
	equal(t.ui.pages.Activity.Visible, true)
	equal(#t.toastFrames(), 0)
end)

test("Review opens the Sync tab", function()
	local t = setup()
	t.ui.widget.Enabled = false
	t.ui:selectTab("Settings")
	t.ui:notify({ kind = "warning", key = "connection", title = "Review needed", sticky = true, action = "review" })
	local action = t.text(t.toastFrames()[1], "Action")
	equal(action.Text, "Review")
	action.Activated:Fire()
	equal(t.ui.widget.Enabled, true)
	equal(t.ui.pages.Sync.Visible, true)
end)

test("Reconnect uses the address in the panel", function()
	local t = setup({ VerdeServerUrl = "wss://example.com/ws" })
	t.ui:setStatus("disconnected", "gone", "Connection lost")
	t.ui:notify({ kind = "error", key = "connection", title = "Connection lost", sticky = true, action = "reconnect" })
	t.text(t.toastFrames()[1], "Action").Activated:Fire()
	equal(t.calls.connects[1], "wss://example.com/ws")
	equal(#t.toastFrames(), 0)
end)

test("dismissing by key closes the pop-up but keeps its history", function()
	local t = setup()
	t.ui:notify({ kind = "error", key = "connection", title = "Lost", sticky = true })
	t.ui:notify({ dismiss = "connection" })
	equal(#t.toastFrames(), 0)
	equal(#t.ui.notifications:all(), 1)
end)

test("long details are shortened without splitting a character", function()
	local t = setup()
	t.ui:notify({ kind = "error", title = "Long", detail = string.rep("é", 400) })
	local text = t.text(t.toastFrames()[1], "Detail").Text
	assert(utf8.len(text), "should stay valid UTF-8")
	equal(utf8.len(text), 181)
	local entry = t.ui.notifications:all()[1]
	equal(utf8.len(entry.detail), 400)
end)

test("closed pop-ups stop repainting", function()
	local t = setup()
	t.ui:notify({ kind = "info", title = "Bye" })
	equal(next(t.ui.theme.scopes) ~= nil, true)
	t.ui.toasts:dismissAll()
	equal(next(t.ui.theme.scopes) ~= nil, true) -- the feed row's scope remains
	t.ui.notifications:clear()
	t.ui:_renderFeed()
	equal(next(t.ui.theme.scopes), nil)
end)

test("pop-ups repaint with the theme", function()
	local t = setup()
	t.ui:notify({ kind = "error", title = "Bad", sticky = true })
	local title = t.text(t.toastFrames()[1], "Title")
	local before = title.TextColor3
	t.setLight()
	assert(title.TextColor3 ~= before)
end)

test("without CoreGui access the feed still works", function()
	local t = setup({ __noCoreGui = true })
	t.ui:notify({ kind = "error", title = "Bad" })
	equal(#t.ui.notifications:all(), 1)
	equal(t.coreGui:FindFirstChild("VerdeNotifications"), nil)
	t.ui:tick(os.clock() + 100, nil)
	t.ui:destroy()
end)

test("unloading removes the pop-ups", function()
	local t = setup()
	t.ui:notify({ kind = "error", title = "Bad", sticky = true })
	local gui = t.coreGui:FindFirstChild("VerdeNotifications")
	t.ui:destroy()
	assert(t.destroyed[gui], "the ScreenGui should be destroyed")
	equal(t.coreGui:FindFirstChild("VerdeNotifications"), nil)
end)

test("leftovers from a copy that didn't unload cleanly are replaced", function()
	local t = setup({ __staleGui = true })
	assert(t.destroyed[t.staleGui], "the old ScreenGui should be destroyed")
	t.ui:notify({ kind = "info", title = "Fresh" })
	equal(#t.toastFrames(), 1)
end)

--------------------------------------------------------------------------------
-- Settings
--------------------------------------------------------------------------------

test("turning pop-ups off is saved, clears them, and keeps recording", function()
	local t = setup()
	t.ui:notify({ kind = "error", title = "Bad", sticky = true })
	t.ui.toggles.popups.button.Activated:Fire()
	equal(t.plugin.saved.VerdePopups, false)
	equal(#t.toastFrames(), 0)
	t.ui:notify({ kind = "error", title = "Worse" })
	equal(#t.toastFrames(), 0)
	equal(#t.ui.notifications:all(), 2)
end)

test("the change setting only silences routine pop-ups", function()
	local t = setup()
	t.ui.toggles.changes.button.Activated:Fire()
	equal(t.plugin.saved.VerdeChangePopups, false)
	t.ui:notify({ kind = "activity", key = "fromDisk", title = "Updated", routine = true })
	equal(#t.toastFrames(), 0)
	equal(#t.ui.notifications:all(), 1)
	t.ui:notify({ kind = "error", title = "Bad" })
	equal(#t.toastFrames(), 1)
end)

test("the change setting can't be flipped while pop-ups are off", function()
	local t = setup()
	t.ui.toggles.popups.button.Activated:Fire()
	t.ui.toggles.changes.button.Activated:Fire()
	equal(t.plugin.saved.VerdeChangePopups, nil)
	equal(t.ui.toggles.changes.value, true)
end)

test("settings load from the plugin, and malformed ones use defaults", function()
	local t = setup({ VerdePopups = false, VerdeChangePopups = "yes" })
	equal(t.ui.toggles.popups.value, false)
	equal(t.ui.toggles.changes.value, true)
	equal(t.ui.toggles.changes.enabled, false)
	t.ui:notify({ kind = "error", title = "Bad" })
	equal(#t.toastFrames(), 0)
end)

print(`UI: {passed} passed, {failed} failed`)
process.exit(if failed > 0 then 1 else 0)
