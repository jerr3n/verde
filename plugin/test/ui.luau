-- Runs the real UI with Roblox property validation and small event/plugin fakes.
-- Lune does not render layouts; resizing and text wrapping still need Studio QA.
local fs = require("@lune/fs")
local luau = require("@lune/luau")
local roblox = require("@lune/roblox")
local process = require("@lune/process")

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
	}
end

local function setup(savedUrl: any?)
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
	local plugin = { saved = savedUrl }
	function plugin:CreateToolbar()
		return {
			CreateButton = function()
				return toggle
			end,
		}
	end
	function plugin:CreateDockWidgetPluginGuiAsync()
		local widget = instance("DockWidgetPluginGui")
		widget.Enabled = true
		return widget
	end
	function plugin:GetSetting()
		return self.saved
	end
	function plugin:SetSetting(_, value)
		self.saved = value
	end
	local environment = {
		Instance = { new = instance },
		Enum = roblox.Enum,
		Color3 = roblox.Color3,
		Vector2 = roblox.Vector2,
		UDim = roblox.UDim,
		UDim2 = roblox.UDim2,
		DockWidgetPluginGuiInfo = {
			new = function(...)
				return { ... }
			end,
		},
		settings = function()
			return { Studio = studio }
		end,
	}
	local Ui = luau.load(fs.readFile("src/Ui.luau"), { environment = environment })()
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
	return {
		ui = ui,
		plugin = plugin,
		calls = calls,
		toggle = toggle,
		studio = studio,
		destroyed = destroyed,
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

test("uses a saved address and ignores malformed settings", function()
	equal(setup("wss://example.com/ws").ui.urlBox.Text, "wss://example.com/ws")
	equal(setup({}).ui.urlBox.Text, "ws://127.0.0.1:34873/ws")
end)

test("invalid addresses stay local and clear their error on edit", function()
	local t = setup()
	for _, address in { "", "   ", "http://localhost", "ws://", "ws:///ws", "ws://local host/ws" } do
		t.ui.urlBox.Text = address
		t.ui.connectButton.Activated:Fire()
		assert(t.ui.inputError.Visible)
	end
	equal(#t.calls.connects, 0)
	equal(t.plugin.saved, nil)
	t.ui.urlBox.Text = "ws://localhost:34873/ws"
	equal(t.ui.inputError.Visible, false)
end)

test("Enter trims and saves the address, and a pending connection can be cancelled", function()
	local t = setup()
	t.ui.urlBox.Text = "  wss://example.com/ws  "
	t.ui.urlBox.FocusLost:Fire(true)
	equal(t.calls.connects[1], "wss://example.com/ws")
	equal(t.plugin.saved, "wss://example.com/ws")
	equal(t.ui.urlBox.TextEditable, false)
	equal(t.ui.connectButton.Text, "Cancel")
	t.ui.urlBox.FocusLost:Fire(true)
	equal(#t.calls.connects, 1)
	t.ui.connectButton.Activated:Fire()
	equal(t.calls.disconnects, 1)
	equal(t.ui.urlBox.TextEditable, true)
end)

test("status details and disconnected help follow the session", function()
	local t = setup()
	t.ui:setStatus("waiting", "Snapshot timed out")
	equal(t.ui.statusDetail.Text, "Snapshot timed out")
	equal(t.ui.help.Visible, false)
	t.ui:setStatus("connected")
	equal(t.ui.connectButton.Text, "Disconnect")
	t.ui:setStatus("disconnected", "Server went away")
	equal(t.ui.statusLabel.Text, "Connection ended")
	equal(t.ui.statusDetail.Text, "Server went away")
	equal(t.ui.help.Visible, true)
end)

test("review shows counts and deletion warning, and only accepts one choice", function()
	local t = setup()
	t.ui:setStatus("choosing")
	t.ui:askChoice({ differ = 12, diskOnly = 3, studioOnly = 4, wouldDeleteFiles = true })
	equal(t.ui.countLabels.differ.Text, "12")
	equal(t.ui.countLabels.diskOnly.Text, "3")
	equal(t.ui.countLabels.studioOnly.Text, "4")
	assert(t.ui.choicePanel.Visible and t.ui.deleteWarning.Visible)
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
	t.studio.ThemeChanged:Fire()
end)

print(`UI: {passed} passed, {failed} failed`)
process.exit(if failed > 0 then 1 else 0)
