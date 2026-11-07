--[[
	The Studio dock widget, with Sync, Activity and Settings tabs. Each page scrolls
	on its own, so small docks keep the top bar in view.
]]

local Kit = require(script.Kit)
local Notifications = require(script.Parent.Notifications)
local Theme = require(script.Theme)
local Toasts = require(script.Toasts)

local Ui = {}
Ui.__index = Ui

local WIDGET_ID = "VerdeWidget"
local URL_SETTING = "VerdeServerUrl"
local POPUPS_SETTING = "VerdePopups"
local CHANGE_POPUPS_SETTING = "VerdeChangePopups"
local DEFAULT_URL = "ws://127.0.0.1:34873/ws"

local BAR_HEIGHT = 38
-- The part of the top bar the logo takes, with the tabs sharing the rest.
local LOGO_AREA = 40
local TAB_NAMES = { "Sync", "Activity", "Settings" }

local STATUS = {
	disconnected = {
		title = "Ready to connect",
		detail = "Connect to a running Verde server to start syncing.",
		tone = "muted",
	},
	connecting = {
		title = "Connecting…",
		detail = "Opening a connection to your server.",
		tone = "warning",
	},
	waiting = {
		title = "Loading project…",
		detail = "Waiting for the server to send your project.",
		tone = "warning",
	},
	choosing = {
		title = "Review before syncing",
		detail = "Disk and Studio differ. Choose which version to keep below.",
		tone = "warning",
	},
	connected = {
		title = "Connected",
		detail = "Changes sync both ways between disk and Studio.",
		tone = "accent",
	},
}

export type Callbacks = {
	onConnect: (url: string) -> (),
	onDisconnect: () -> (),
	onChoose: (side: string) -> (),
}

local function duration(seconds: number): string
	local whole = math.max(0, math.floor(seconds))
	if whole < 60 then
		return `{whole}s`
	elseif whole < 3600 then
		return `{math.floor(whole / 60)}m`
	end
	return `{math.floor(whole / 3600)}h {math.floor(whole % 3600 / 60)}m`
end

local function boolSetting(plugin: Plugin, name: string, default: boolean): boolean
	local value = plugin:GetSetting(name)
	return if type(value) == "boolean" then value else default
end

function Ui:_buildBar(root: Instance)
	local top = Kit.create("Frame", {
		Name = "TopBar",
		Size = UDim2.new(1, 0, 0, BAR_HEIGHT),
		BackgroundTransparency = 1,
	}, root)
	self.logo = Kit.create("ImageLabel", {
		Name = "Logo",
		AnchorPoint = Vector2.new(0.5, 0.5),
		Position = UDim2.new(0, LOGO_AREA / 2, 0.5, 0),
		Size = UDim2.fromOffset(22, 22),
		BackgroundTransparency = 1,
		Image = Kit.LOGO,
	}, top)
	local bar = Kit.create("Frame", {
		Name = "Tabs",
		Position = UDim2.fromOffset(LOGO_AREA, 0),
		Size = UDim2.new(1, -LOGO_AREA, 1, 0),
		BackgroundTransparency = 1,
	}, top)
	self.tabButtons = {}
	for index, name in TAB_NAMES do
		local button = Kit.create("TextButton", {
			Name = name,
			Position = UDim2.fromScale((index - 1) / #TAB_NAMES, 0),
			Size = UDim2.new(1 / #TAB_NAMES, 0, 1, 0),
			BackgroundTransparency = 1,
			AutoButtonColor = false,
			Font = Kit.MEDIUM,
			TextSize = 13,
			Text = name,
		}, bar)
		self.theme:bind(function(colors)
			button.TextColor3 = if self.tab == name then colors.text else colors.muted
		end)
		button.Activated:Connect(function()
			self:selectTab(name)
		end)
		self.tabButtons[name] = button
	end

	self.badge = Kit.create("TextLabel", {
		Name = "Unread",
		AnchorPoint = Vector2.new(1, 0.5),
		Position = UDim2.new(1, -1, 0.5, 0),
		Size = UDim2.fromOffset(16, 14),
		BorderSizePixel = 0,
		Font = Kit.BOLD,
		TextSize = 10,
		Text = "",
		Visible = false,
	}, self.tabButtons.Activity)
	Kit.corner(self.badge, 7)
	self.theme:bind(function(colors)
		self.badge.BackgroundColor3 = colors.error
		self.badge.TextColor3 = colors.background
	end)

	self.tabIndicator = Kit.create("Frame", {
		Name = "Selected",
		AnchorPoint = Vector2.new(0, 1),
		Position = UDim2.fromScale(0, 1),
		Size = UDim2.new(1 / #TAB_NAMES, 0, 0, 2),
		BorderSizePixel = 0,
		ZIndex = 2,
	}, bar)
	local divider = Kit.create("Frame", {
		Name = "Divider",
		AnchorPoint = Vector2.new(0, 1),
		Position = UDim2.fromScale(0, 1),
		Size = UDim2.new(1, 0, 0, 1),
		BorderSizePixel = 0,
	}, top)
	self.theme:bind(function(colors)
		self.tabIndicator.BackgroundColor3 = colors.accent
		divider.BackgroundColor3 = colors.border
	end)
end

function Ui:_buildPage(root: Instance, name: string): any
	local scroll = Kit.create("ScrollingFrame", {
		Name = name,
		Position = UDim2.fromOffset(0, BAR_HEIGHT),
		Size = UDim2.new(1, 0, 1, -BAR_HEIGHT),
		BackgroundTransparency = 1,
		BorderSizePixel = 0,
		CanvasSize = UDim2.new(),
		AutomaticCanvasSize = Enum.AutomaticSize.Y,
		ScrollingDirection = Enum.ScrollingDirection.Y,
		ScrollBarThickness = 4,
		VerticalScrollBarInset = Enum.ScrollBarInset.ScrollBar,
		Visible = false,
	}, root)
	self.theme:bind(function(colors)
		scroll.ScrollBarImageColor3 = colors.muted
	end)
	-- A growing inner frame includes the bottom padding in the scroll bounds.
	local content = Kit.stack(scroll, "Content", 14, 1)
	Kit.padding(content, 14, 12)
	self.pages[name] = scroll
	return content
end

function Ui:_infoRow(parent: Instance, name: string, caption: string, order: number): any
	local row = Kit.create("Frame", {
		Name = name,
		Size = UDim2.fromScale(1, 0),
		AutomaticSize = Enum.AutomaticSize.Y,
		BackgroundTransparency = 1,
		LayoutOrder = order,
	}, parent)
	local title = Kit.label(self.theme, row, "Caption", caption, { size = 13, tone = "muted" })
	title.Size = UDim2.fromOffset(78, 0)
	local value = Kit.label(self.theme, row, "Value", "", { size = 13 })
	value.Position = UDim2.fromOffset(82, 0)
	value.Size = UDim2.new(1, -82, 0, 0)
	return value
end

function Ui:_buildStatusCard(parent: Instance)
	local outer = Kit.create("Frame", {
		Name = "Status",
		Size = UDim2.fromScale(1, 0),
		AutomaticSize = Enum.AutomaticSize.Y,
		BorderSizePixel = 0,
		LayoutOrder = 1,
	}, parent)
	Kit.corner(outer, 8)
	local stroke = Kit.create("UIStroke", { ApplyStrokeMode = Enum.ApplyStrokeMode.Border, Thickness = 1 }, outer)
	local stripe = Kit.create("Frame", {
		Name = "Tone",
		Position = UDim2.fromOffset(7, 9),
		Size = UDim2.new(0, 3, 1, -18),
		BorderSizePixel = 0,
	}, outer)
	Kit.corner(stripe, 2)
	local card = Kit.stack(outer, "Content", 4, 1)
	Kit.create("UIPadding", {
		PaddingTop = UDim.new(0, 11),
		PaddingBottom = UDim.new(0, 11),
		PaddingLeft = UDim.new(0, 18),
		PaddingRight = UDim.new(0, 12),
	}, card)

	self.statusLabel = Kit.label(self.theme, card, "Title", "", { size = 15, font = Kit.BOLD, order = 1 })
	self.statusDetail = Kit.label(self.theme, card, "Detail", "", { size = 13, tone = "muted", order = 2 })
	self.theme:bind(function(colors)
		local tone = self:_tone()
		outer.BackgroundColor3 = colors.surface
		stroke.Color = colors.border
		stripe.BackgroundColor3 = colors[tone]
		self.statusLabel.TextColor3 = if tone == "muted" then colors.text else colors[tone]
	end)

	self.infoSection = Kit.stack(card, "Details", 5, 3)
	self.infoSection.Visible = false
	local spacer = Kit.create("Frame", {
		Name = "Divider",
		Size = UDim2.new(1, 0, 0, 1),
		BorderSizePixel = 0,
		LayoutOrder = 0,
	}, self.infoSection)
	self.theme:bind(function(colors)
		spacer.BackgroundColor3 = colors.border
	end)
	self.infoRows = {
		server = self:_infoRow(self.infoSection, "Server", "Server", 1),
		mounts = self:_infoRow(self.infoSection, "Folders", "Syncing", 2),
		uptime = self:_infoRow(self.infoSection, "Uptime", "Connected", 3),
		transfer = self:_infoRow(self.infoSection, "Transfer", "Changes", 4),
		last = self:_infoRow(self.infoSection, "Last", "Last change", 5),
	}
end

function Ui:_buildConnection(parent: Instance, plugin: Plugin, callbacks: Callbacks)
	local section = Kit.stack(parent, "Connection", 8, 2)
	self.addressSection = Kit.stack(section, "Address", 6, 1)
	Kit.label(self.theme, self.addressSection, "Caption", "Server address", {
		size = 13,
		font = Kit.MEDIUM,
		order = 1,
	})
	local savedUrl = plugin:GetSetting(URL_SETTING)
	local urlBox = Kit.create("TextBox", {
		Name = "ServerAddress",
		Size = UDim2.new(1, 0, 0, 34),
		ClearTextOnFocus = false,
		MultiLine = false,
		Font = Kit.MONO,
		TextSize = 13,
		TextXAlignment = Enum.TextXAlignment.Left,
		TextTruncate = Enum.TextTruncate.AtEnd,
		Text = if type(savedUrl) == "string" then savedUrl else DEFAULT_URL,
		PlaceholderText = DEFAULT_URL,
		BorderSizePixel = 0,
		LayoutOrder = 2,
	}, self.addressSection)
	Kit.padding(urlBox, 0, 10)
	Kit.corner(urlBox, 6)
	local stroke = Kit.create("UIStroke", { ApplyStrokeMode = Enum.ApplyStrokeMode.Border, Thickness = 1 }, urlBox)
	local focused = false
	self.theme:bind(function(colors)
		urlBox.BackgroundColor3 = colors.input
		urlBox.TextColor3 = if self.connected then colors.muted else colors.text
		urlBox.PlaceholderColor3 = colors.muted
		stroke.Color = if self.validationError then colors.error else (if focused then colors.accent else colors.border)
	end)
	self.urlBox = urlBox
	self.inputError = Kit.label(self.theme, self.addressSection, "AddressError", "", {
		size = 13,
		tone = "error",
		order = 3,
	})
	self.inputError.Visible = false

	self.connectButton = Kit.button(self.theme, section, "Connect", "Connect", 2, function()
		return if self.status == "disconnected" then "primary" else "secondary"
	end)

	local function connect()
		if self.connected then
			return
		end
		local url = string.match(urlBox.Text, "^%s*(.-)%s*$") or ""
		if not string.match(url, "^wss?://[^/%s?#]+[^%s]*$") then
			self.validationError = "Enter a server address starting with ws:// or wss://."
			self.inputError.Text = self.validationError
			self.inputError.Visible = true
			self.theme:refresh()
			return
		end
		urlBox.Text = url
		plugin:SetSetting(URL_SETTING, url)
		callbacks.onConnect(url)
	end
	self.connect = connect

	self.connectButton.Activated:Connect(function()
		if self.connected then
			callbacks.onDisconnect()
		else
			connect()
		end
	end)
	urlBox.Focused:Connect(function()
		focused = true
		self.theme:refresh()
	end)
	urlBox.FocusLost:Connect(function(enterPressed)
		focused = false
		self.theme:refresh()
		if enterPressed then
			connect()
		end
	end)
	urlBox:GetPropertyChangedSignal("Text"):Connect(function()
		if self.validationError then
			self.validationError = nil
			self.inputError.Visible = false
			self.theme:refresh()
		end
	end)
end

function Ui:_buildReview(parent: Instance, callbacks: Callbacks)
	self.choicePanel = Kit.card(self.theme, parent, "Review", 10, 3)
	self.choicePanel.Visible = false
	Kit.label(self.theme, self.choicePanel, "Title", "Choose a starting version", {
		size = 15,
		font = Kit.BOLD,
		order = 1,
	})

	local counts = Kit.create("Frame", {
		Name = "Summary",
		Size = UDim2.new(1, 0, 0, 62),
		BackgroundTransparency = 1,
		LayoutOrder = 2,
	}, self.choicePanel)
	self.countLabels = {}
	for index, item in
		{ { "differ", "Properties differ" }, { "diskOnly", "Only on disk" }, { "studioOnly", "Only in Studio" } }
	do
		local cell = Kit.create("Frame", {
			Name = item[1],
			Position = UDim2.fromScale((index - 1) / 3, 0),
			Size = UDim2.new(1 / 3, -6, 1, 0),
			BackgroundTransparency = 1,
		}, counts)
		local value = Kit.label(self.theme, cell, "Count", "0", { size = 22, font = Kit.BOLD })
		local caption = Kit.label(self.theme, cell, "Caption", item[2], { size = 12, tone = "muted" })
		caption.Position = UDim2.fromOffset(0, 28)
		self.countLabels[item[1]] = value
	end

	self.keepDisk = Kit.button(self.theme, self.choicePanel, "KeepDisk", "Keep disk", 3, function()
		return "secondary"
	end)
	Kit.label(self.theme, self.choicePanel, "KeepDiskDetail", "Replace synced Studio content with the disk version.", {
		size = 13,
		tone = "muted",
		order = 4,
	})
	self.keepStudio = Kit.button(self.theme, self.choicePanel, "KeepStudio", "Keep Studio", 5, function()
		return if self.reviewDeletes then "danger" else "secondary"
	end)
	Kit.label(self.theme, self.choicePanel, "KeepStudioDetail", "Write the Studio version to disk.", {
		size = 13,
		tone = "muted",
		order = 6,
	})
	self.deleteWarning = Kit.label(self.theme, self.choicePanel, "DeleteWarning", "", {
		size = 13,
		tone = "warning",
		order = 7,
	})
	self.deleteWarning.Visible = false

	local function choose(side: string)
		if self.status ~= "choosing" or self.choicePending then
			return
		end
		self.choicePending = true
		self.keepDisk.Active = false
		self.keepStudio.Active = false
		self.keepDisk.Selectable = false
		self.keepStudio.Selectable = false
		local button = if side == "disk" then self.keepDisk else self.keepStudio
		button.Text = "Applying…"
		self.theme:refresh()
		callbacks.onChoose(side)
	end
	self.keepDisk.Activated:Connect(function()
		choose("disk")
	end)
	self.keepStudio.Activated:Connect(function()
		choose("studio")
	end)
end

function Ui:_buildHelp(parent: Instance)
	self.help = Kit.card(self.theme, parent, "GettingStarted", 4, 4)
	Kit.label(self.theme, self.help, "Title", "Start your server", { size = 13, font = Kit.BOLD, order = 1 })
	Kit.label(self.theme, self.help, "Description", "Run this in your project folder, then connect.", {
		size = 13,
		tone = "muted",
		order = 2,
	})
	Kit.label(self.theme, self.help, "Command", "verde serve", {
		size = 13,
		tone = "accent",
		font = Kit.MONO,
		order = 3,
	})
end

function Ui:_buildActivity(parent: Instance)
	local toolbar = Kit.create("Frame", {
		Name = "Toolbar",
		Size = UDim2.new(1, 0, 0, 26),
		BackgroundTransparency = 1,
		LayoutOrder = 1,
	}, parent)
	local caption = Kit.label(self.theme, toolbar, "Caption", "Recent activity", { size = 13, font = Kit.BOLD })
	caption.AnchorPoint = Vector2.new(0, 0.5)
	caption.Position = UDim2.fromScale(0, 0.5)
	self.clearButton = Kit.button(self.theme, toolbar, "Clear", "Clear", 1, function()
		return "secondary"
	end)
	self.clearButton.AnchorPoint = Vector2.new(1, 0)
	self.clearButton.Position = UDim2.fromScale(1, 0)
	self.clearButton.Size = UDim2.fromOffset(56, 26)
	self.clearButton.TextSize = 13
	self.clearButton.Activated:Connect(function()
		self.notifications:clear()
		self:_renderFeed()
	end)

	self.feedEmpty = Kit.label(self.theme, parent, "Empty", "Nothing yet. Connections and sync events show up here.", {
		size = 13,
		tone = "muted",
		order = 2,
	})
	self.feed = Kit.stack(parent, "Feed", 10, 3)
end

function Ui:_buildRow(entry: any): any
	local scope = self.theme:scope()
	local frame = Kit.create("Frame", {
		Name = "Entry",
		Size = UDim2.fromScale(1, 0),
		AutomaticSize = Enum.AutomaticSize.Y,
		BackgroundTransparency = 1,
	}, self.feed)
	local dot = Kit.create("Frame", {
		Name = "Tone",
		Position = UDim2.fromOffset(0, 5),
		Size = UDim2.fromOffset(7, 7),
		BorderSizePixel = 0,
	}, frame)
	Kit.corner(dot, 4)
	local text = Kit.stack(frame, "Text", 1, 1)
	text.Position = UDim2.fromOffset(14, 0)
	text.Size = UDim2.new(1, -72, 0, 0)
	local row = {
		frame = frame,
		scope = scope,
		kind = entry.kind,
		title = Kit.label(scope, text, "Title", "", { size = 13, font = Kit.MEDIUM, order = 1 }),
		detail = Kit.label(scope, text, "Detail", "", { size = 12, tone = "muted", order = 2 }),
		time = Kit.label(scope, frame, "Time", "", { size = 11, tone = "muted" }),
	}
	row.time.AnchorPoint = Vector2.new(1, 0)
	row.time.Position = UDim2.new(1, 0, 0, 1)
	row.time.Size = UDim2.fromOffset(54, 0)
	row.time.TextXAlignment = Enum.TextXAlignment.Right
	scope:bind(function(colors)
		dot.BackgroundColor3 = colors[Theme.TONES[row.kind] or "muted"]
	end)
	return row
end

-- Brings the feed's rows and the unread counts in line with the history.
function Ui:_renderFeed()
	local seen = {}
	for _, entry in self.notifications:all() do
		seen[entry.id] = true
		local row = self.rows[entry.id]
		if row == nil then
			row = self:_buildRow(entry)
			self.rows[entry.id] = row
		end
		row.frame.LayoutOrder = -entry.order
		row.title.Text = entry.title
		row.detail.Text = entry.detail or ""
		row.detail.Visible = entry.detail ~= nil and entry.detail ~= ""
		row.time.Text = os.date("%H:%M:%S", entry.time)
		if row.kind ~= entry.kind then
			row.kind = entry.kind
			row.scope:refresh()
		end
	end
	for id, row in self.rows do
		if not seen[id] then
			row.scope:destroy()
			row.frame:Destroy()
			self.rows[id] = nil
		end
	end
	self.feedEmpty.Visible = #self.notifications:all() == 0

	if self.tab == "Activity" and self.widget.Enabled then
		self.notifications:markRead()
	end
	local unread = self.notifications:unread()
	self.badge.Text = if unread > 9 then "9+" else tostring(unread)
	self.badge.Visible = unread > 0
	self:_updateTitle()
end

function Ui:_toggleRow(
	parent: Instance,
	name: string,
	title: string,
	detail: string,
	order: number,
	value: boolean
): any
	local row = Kit.create("TextButton", {
		Name = name,
		Size = UDim2.fromScale(1, 0),
		AutomaticSize = Enum.AutomaticSize.Y,
		BackgroundTransparency = 1,
		BorderSizePixel = 0,
		AutoButtonColor = false,
		Text = "",
		LayoutOrder = order,
	}, parent)
	local text = Kit.stack(row, "Text", 2, 1)
	text.Size = UDim2.new(1, -48, 0, 0)
	Kit.label(self.theme, text, "Title", title, { size = 14, font = Kit.MEDIUM, order = 1 })
	Kit.label(self.theme, text, "Detail", detail, { size = 12, tone = "muted", order = 2 })
	local track = Kit.create("Frame", {
		Name = "Track",
		AnchorPoint = Vector2.new(1, 0),
		Position = UDim2.new(1, 0, 0, 2),
		Size = UDim2.fromOffset(36, 20),
		BorderSizePixel = 0,
	}, row)
	Kit.corner(track, 10)
	local knob = Kit.create("Frame", {
		Name = "Knob",
		Size = UDim2.fromOffset(16, 16),
		BorderSizePixel = 0,
	}, track)
	Kit.corner(knob, 8)

	local toggle = { button = row, value = value, enabled = true }
	local function knobPosition()
		return UDim2.fromOffset(if toggle.value then 18 else 2, 2)
	end
	knob.Position = knobPosition()
	self.theme:bind(function(colors)
		local on = toggle.value and toggle.enabled
		track.BackgroundColor3 = if on then colors.primary else colors.raised
		knob.BackgroundColor3 = if on then colors.onPrimary else colors.muted
		track.BackgroundTransparency = if toggle.enabled then 0 else 0.5
	end)
	function toggle.set(new: boolean)
		toggle.value = new
		Kit.tween(knob, { Position = knobPosition() })
		self.theme:refresh()
	end
	return toggle
end

function Ui:_buildSettings(parent: Instance, plugin: Plugin)
	local popups = self:_toggleRow(
		parent,
		"Popups",
		"Pop-up notifications",
		"Show notifications over the 3D view. The Activity tab always records them.",
		1,
		boolSetting(plugin, POPUPS_SETTING, true)
	)
	local changes = self:_toggleRow(
		parent,
		"ChangePopups",
		"Changes from disk",
		"Pop up when a change on disk updates Studio. Problems always pop up.",
		2,
		boolSetting(plugin, CHANGE_POPUPS_SETTING, true)
	)
	self.toggles = { popups = popups, changes = changes }
	changes.enabled = popups.value

	popups.button.Activated:Connect(function()
		popups.set(not popups.value)
		changes.enabled = popups.value
		plugin:SetSetting(POPUPS_SETTING, popups.value)
		if not popups.value then
			self.toasts:dismissAll()
		end
		self.theme:refresh()
	end)
	changes.button.Activated:Connect(function()
		if changes.enabled then
			changes.set(not changes.value)
			plugin:SetSetting(CHANGE_POPUPS_SETTING, changes.value)
		end
	end)
end

function Ui.new(plugin: Plugin, callbacks: Callbacks)
	local self = setmetatable({
		connected = false,
		status = "disconnected",
		detail = nil :: string?,
		titleOverride = nil :: string?,
		validationError = nil :: string?,
		choicePending = false,
		reviewDeletes = false,
		tab = "Sync",
		pages = {} :: { [string]: any },
		rows = {} :: { [number]: any },
		theme = Theme.new(),
		notifications = Notifications.new(),
	}, Ui)

	local toolbar = plugin:CreateToolbar("Verde")
	local toggle = toolbar:CreateButton("Verde", "Show or hide the Verde sync panel", Kit.LOGO)
	toggle.ClickableWhenViewportHidden = true
	local widget = plugin:CreateDockWidgetPluginGuiAsync(
		WIDGET_ID,
		DockWidgetPluginGuiInfo.new(Enum.InitialDockState.Float, true, false, 340, 480, 280, 240)
	)
	widget.Title = "Verde"
	widget.Name = WIDGET_ID
	self.widget = widget
	toggle:SetActive(widget.Enabled)
	toggle.Click:Connect(function()
		widget.Enabled = not widget.Enabled
	end)
	widget:GetPropertyChangedSignal("Enabled"):Connect(function()
		toggle:SetActive(widget.Enabled)
		self:_renderFeed()
	end)

	self.toasts = Toasts.new(self.theme, {
		onOpen = function(entry)
			self:reveal(if entry.action == "review" then "Sync" else "Activity")
		end,
		onAction = function(entry)
			if entry.action == "reconnect" then
				self:reveal("Sync")
				self.connect()
			elseif entry.action == "review" then
				self:reveal("Sync")
			end
		end,
	})

	local root = Kit.create("Frame", {
		Name = "Root",
		Size = UDim2.fromScale(1, 1),
		BorderSizePixel = 0,
	}, widget)
	self.theme:bind(function(colors)
		root.BackgroundColor3 = colors.background
	end)

	self:_buildBar(root)

	local syncPage = self:_buildPage(root, "Sync")
	self:_buildStatusCard(syncPage)
	self:_buildConnection(syncPage, plugin, callbacks)
	self:_buildReview(syncPage, callbacks)
	self:_buildHelp(syncPage)
	self:_buildActivity(self:_buildPage(root, "Activity"))
	self:_buildSettings(self:_buildPage(root, "Settings"), plugin)

	self:selectTab("Sync")
	self:setStatus("disconnected", nil)
	self:_renderFeed()
	return self
end

function Ui:_tone(): string
	if self.status == "disconnected" and self.detail then
		return "error"
	end
	return (STATUS[self.status] or STATUS.disconnected).tone
end

-- A docked panel's tab shows its title, even when another tab is on top, so problems
-- are still noticed from a script tab.
function Ui:_updateTitle()
	local unread = self.notifications:unread()
	self.widget.Title = if unread > 0 then `Verde ({unread})` else "Verde"
end

function Ui:selectTab(name: string)
	self.tab = name
	for pageName, page in self.pages do
		page.Visible = pageName == name
	end
	local index = table.find(TAB_NAMES, name) :: number
	Kit.tween(self.tabIndicator, {
		Position = UDim2.fromScale((index - 1) / #TAB_NAMES, 1),
	})
	self.theme:refresh()
	self:_renderFeed()
end

-- Opens the panel on a tab.
function Ui:reveal(tab: string)
	self.widget.Enabled = true
	self:selectTab(tab)
end

function Ui:setStatus(status: string, detail: string?, title: string?)
	self.status = status
	self.detail = if detail and detail ~= "" then detail else nil
	self.titleOverride = title
	self.connected = status ~= "disconnected"

	self.connectButton.Text = if status == "connected"
		then "Disconnect"
		else (if self.connected then "Cancel" else "Connect")
	self.urlBox.TextEditable = not self.connected
	self.addressSection.Visible = status ~= "connected"
	self.help.Visible = status == "disconnected"
	self.infoSection.Visible = status == "connected"

	local state = STATUS[status] or { title = status, detail = "", tone = "muted" }
	self.statusLabel.Text = title
		or (if status == "disconnected" and self.detail then "Connection ended" else state.title)
	self.statusDetail.Text = self.detail or state.detail
	if status ~= "choosing" then
		self.choicePanel.Visible = false
	end

	self:_updateTitle()
	self.theme:refresh()
end

function Ui:askChoice(summary: { differ: number, diskOnly: number, studioOnly: number, wouldDeleteFiles: boolean })
	self.choicePending = false
	self.reviewDeletes = summary.wouldDeleteFiles
	self.keepDisk.Text = "Keep disk"
	self.keepStudio.Text = "Keep Studio"
	self.keepDisk.Active = true
	self.keepStudio.Active = true
	self.keepDisk.Selectable = true
	self.keepStudio.Selectable = true
	self.countLabels.differ.Text = tostring(summary.differ)
	self.countLabels.diskOnly.Text = tostring(summary.diskOnly)
	self.countLabels.studioOnly.Text = tostring(summary.studioOnly)
	self.deleteWarning.Text = if summary.diskOnly == 1
		then "Keeping Studio removes 1 instance from disk. Its files move to .verde/trash."
		else `Keeping Studio removes {summary.diskOnly} instances from disk. Their files move to .verde/trash.`
	self.deleteWarning.Visible = summary.wouldDeleteFiles
	self.choicePanel.Visible = true
	-- Nothing syncs until this is answered, so it can't wait behind a closed panel.
	self:reveal("Sync")
end

-- Records a notice from the session and pops it up if the settings allow.
function Ui:notify(notice: any)
	if notice.dismiss then
		self.toasts:dismiss(notice.dismiss)
		return
	end
	local entry = self.notifications:push(notice)
	self:_renderFeed()
	local popups = self.toggles.popups.value
	local allowed = not notice.routine or self.toggles.changes.value
	if popups and allowed and not notice.quiet then
		self.toasts:show(entry)
	end
end

-- Runs one step. info comes from Session:info(), and is nil unless connected.
function Ui:tick(now: number, info: any)
	self.toasts:tick(now)
	if info then
		self.infoRows.server.Text = info.url
		self.infoRows.mounts.Text = if #info.mounts > 0 then table.concat(info.mounts, ", ") else "Nothing"
		self.infoRows.uptime.Text = `for {duration(now - info.connectedAt)}`
		self.infoRows.transfer.Text = `{info.fromDisk} from disk, {info.toDisk} to disk`
		self.infoRows.last.Text = if info.lastChangeAt then `{duration(now - info.lastChangeAt)} ago` else "None yet"
	end
end

function Ui:destroy()
	self.toasts:destroy()
	self.theme:destroy()
	self.widget:Destroy()
end

return Ui
