--[[
	The Studio dock widget. Layout grows with its content and scrolls in small
	docks; colors follow Studio's theme with Verde accents for sync states.
]]

local Ui = {}
Ui.__index = Ui

local WIDGET_ID = "VerdeWidget"
local URL_SETTING = "VerdeServerUrl"
local DEFAULT_URL = "ws://127.0.0.1:34873/ws"

local STATUS = {
	disconnected = {
		title = "Ready to connect",
		detail = "Connect to a running Verde server to start syncing.",
		tone = "muted",
	},
	connecting = { title = "Connecting…", detail = "Opening a connection to your server.", tone = "warning" },
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
	connected = { title = "Connected", detail = "Changes sync both ways between disk and Studio.", tone = "accent" },
}

export type Callbacks = {
	onConnect: (url: string) -> (),
	onDisconnect: () -> (),
	onChoose: (side: string) -> (),
}

local function create(className: string, properties: { [string]: any }, parent: Instance?): any
	local object = Instance.new(className)
	for key, value in properties do
		(object :: any)[key] = value
	end
	object.Parent = parent
	return object
end

local function round(object: GuiObject, radius: number)
	create("UICorner", { CornerRadius = UDim.new(0, radius) }, object)
end

local function pad(object: GuiObject, amount: number)
	create("UIPadding", {
		PaddingTop = UDim.new(0, amount),
		PaddingBottom = UDim.new(0, amount),
		PaddingLeft = UDim.new(0, amount),
		PaddingRight = UDim.new(0, amount),
	}, object)
end

local function stack(parent: Instance, name: string, gap: number, order: number): Frame
	local frame = create("Frame", {
		Name = name,
		Size = UDim2.fromScale(1, 0),
		AutomaticSize = Enum.AutomaticSize.Y,
		BackgroundTransparency = 1,
		BorderSizePixel = 0,
		LayoutOrder = order,
	}, parent)
	create("UIListLayout", {
		Padding = UDim.new(0, gap),
		SortOrder = Enum.SortOrder.LayoutOrder,
	}, frame)
	return frame
end

local function palette(): { [string]: Color3 }
	local theme = settings().Studio.Theme
	local background = theme:GetColor(Enum.StudioStyleGuideColor.MainBackground)
	local dark = background.R + background.G + background.B < 1.5
	local ink = theme:GetColor(Enum.StudioStyleGuideColor.MainText)
	return {
		background = background,
		surface = background:Lerp(ink, if dark then 0.035 else 0.025),
		input = theme:GetColor(Enum.StudioStyleGuideColor.InputFieldBackground),
		border = background:Lerp(ink, if dark then 0.16 else 0.18),
		text = ink,
		muted = background:Lerp(ink, if dark then 0.61 else 0.67),
		accent = if dark then Color3.fromRGB(77, 206, 132) else Color3.fromRGB(20, 121, 67),
		primary = Color3.fromRGB(26, 132, 76),
		primaryHover = Color3.fromRGB(22, 115, 65),
		onPrimary = Color3.new(1, 1, 1),
		warning = if dark then Color3.fromRGB(238, 187, 91) else Color3.fromRGB(145, 93, 15),
		error = if dark then Color3.fromRGB(247, 137, 137) else Color3.fromRGB(183, 48, 48),
	}
end

function Ui:_theme(apply: (colors: { [string]: Color3 }) -> ())
	table.insert(self.themed, apply)
end

function Ui:_applyTheme()
	local colors = palette()
	for _, apply in self.themed do
		apply(colors)
	end
end

function Ui:_label(parent: Instance, name: string, text: string, size: number, tone: string, order: number): TextLabel
	local label = create("TextLabel", {
		Name = name,
		Size = UDim2.fromScale(1, 0),
		AutomaticSize = Enum.AutomaticSize.Y,
		BackgroundTransparency = 1,
		Font = Enum.Font.SourceSans,
		TextSize = size,
		Text = text,
		TextWrapped = true,
		TextXAlignment = Enum.TextXAlignment.Left,
		TextYAlignment = Enum.TextYAlignment.Top,
		LayoutOrder = order,
	}, parent)
	self:_theme(function(colors)
		label.TextColor3 = colors[tone]
	end)
	return label
end

function Ui:_button(parent: Instance, name: string, text: string, order: number, primary: () -> boolean): TextButton
	local button = create("TextButton", {
		Name = name,
		Size = UDim2.new(1, 0, 0, 38),
		BorderSizePixel = 0,
		AutoButtonColor = false,
		Font = Enum.Font.SourceSansSemibold,
		TextSize = 16,
		Text = text,
		LayoutOrder = order,
	}, parent)
	round(button, 6)
	local stroke = create("UIStroke", { ApplyStrokeMode = Enum.ApplyStrokeMode.Border, Thickness = 1 }, button)
	local hovered = false
	local function paint(colors)
		local disabled = not button.Active
		local filled = primary() and not disabled
		button.BackgroundColor3 = if filled
			then (if hovered then colors.primaryHover else colors.primary)
			else (if hovered and not disabled then colors.surface:Lerp(colors.text, 0.06) else colors.surface)
		button.TextColor3 = if disabled then colors.muted else (if filled then colors.onPrimary else colors.text)
		stroke.Color = if filled then colors.primary else colors.border
	end
	self:_theme(paint)
	button.MouseEnter:Connect(function()
		hovered = true
		paint(palette())
	end)
	button.MouseLeave:Connect(function()
		hovered = false
		paint(palette())
	end)
	return button
end

function Ui.new(plugin: Plugin, callbacks: Callbacks)
	local self = setmetatable({
		connected = false,
		status = "disconnected",
		validationError = nil :: string?,
		choicePending = false,
		themed = {} :: { (colors: { [string]: Color3 }) -> () },
	}, Ui)

	local toolbar = plugin:CreateToolbar("Verde")
	local toggle = toolbar:CreateButton("Verde", "Show or hide the Verde sync panel", "")
	toggle.ClickableWhenViewportHidden = true
	local widget = plugin:CreateDockWidgetPluginGuiAsync(
		WIDGET_ID,
		DockWidgetPluginGuiInfo.new(Enum.InitialDockState.Float, true, false, 360, 460, 280, 240)
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
	end)

	local scroll = create("ScrollingFrame", {
		Name = "Scroll",
		Size = UDim2.fromScale(1, 1),
		BorderSizePixel = 0,
		CanvasSize = UDim2.new(),
		AutomaticCanvasSize = Enum.AutomaticSize.Y,
		ScrollingDirection = Enum.ScrollingDirection.Y,
		ScrollBarThickness = 4,
		VerticalScrollBarInset = Enum.ScrollBarInset.ScrollBar,
	}, widget)
	-- A growing inner frame includes the bottom padding in the scroll bounds.
	local root = stack(scroll, "Content", 20, 1)
	pad(root, 20)
	self:_theme(function(colors)
		scroll.BackgroundColor3 = colors.background
		scroll.ScrollBarImageColor3 = colors.muted
	end)

	local header = create("Frame", {
		Name = "Header",
		Size = UDim2.new(1, 0, 0, 44),
		BackgroundTransparency = 1,
		LayoutOrder = 1,
	}, root)
	local mark = create("Frame", {
		Name = "Mark",
		Position = UDim2.fromOffset(0, 4),
		Size = UDim2.fromOffset(36, 36),
		BorderSizePixel = 0,
	}, header)
	round(mark, 9)
	self:_theme(function(colors)
		mark.BackgroundColor3 = colors.background:Lerp(colors.accent, 0.12)
	end)
	-- The V-leaf mark, drawn locally so it needs no uploaded image asset.
	local points = {
		Vector2.new(8, 10),
		Vector2.new(15, 27),
		Vector2.new(14, 20),
		Vector2.new(16, 13),
		Vector2.new(21, 9),
		Vector2.new(27, 7),
		Vector2.new(27, 14),
		Vector2.new(25, 20),
		Vector2.new(21, 24),
		Vector2.new(15, 27),
	}
	for index = 2, #points do
		local from, to = points[index - 1], points[index]
		local delta = to - from
		local line = create("Frame", {
			Name = "LeafStroke",
			AnchorPoint = Vector2.new(0.5, 0.5),
			Position = UDim2.fromOffset((from.X + to.X) / 2, (from.Y + to.Y) / 2),
			Size = UDim2.fromOffset(delta.Magnitude + 1, 2),
			Rotation = math.deg(math.atan2(delta.Y, delta.X)),
			BorderSizePixel = 0,
		}, mark)
		round(line, 1)
		self:_theme(function(colors)
			line.BackgroundColor3 = colors.accent
		end)
	end
	local title = self:_label(header, "Title", "Verde", 25, "text", 1)
	title.Font = Enum.Font.SourceSansSemibold
	title.Position = UDim2.fromOffset(48, -2)
	title.Size = UDim2.new(1, -48, 0, 0)
	local subtitle = self:_label(header, "Subtitle", "Two-way project sync", 13, "muted", 2)
	subtitle.Position = UDim2.fromOffset(49, 26)
	subtitle.Size = UDim2.new(1, -49, 0, 0)

	local connection = stack(root, "Connection", 9, 2)
	local addressLabel = self:_label(connection, "AddressLabel", "Server address", 14, "text", 1)
	addressLabel.Font = Enum.Font.SourceSansSemibold
	local savedUrl = plugin:GetSetting(URL_SETTING)
	local urlBox = create("TextBox", {
		Name = "ServerAddress",
		Size = UDim2.new(1, 0, 0, 40),
		ClearTextOnFocus = false,
		MultiLine = false,
		Font = Enum.Font.Code,
		TextSize = 13,
		TextXAlignment = Enum.TextXAlignment.Left,
		Text = if type(savedUrl) == "string" then savedUrl else DEFAULT_URL,
		PlaceholderText = DEFAULT_URL,
		BorderSizePixel = 0,
		LayoutOrder = 2,
	}, connection)
	pad(urlBox, 11)
	round(urlBox, 6)
	local inputStroke = create("UIStroke", { ApplyStrokeMode = Enum.ApplyStrokeMode.Border, Thickness = 1 }, urlBox)
	local focused = false
	self:_theme(function(colors)
		urlBox.BackgroundColor3 = colors.input
		urlBox.TextColor3 = if self.connected then colors.muted else colors.text
		urlBox.PlaceholderColor3 = colors.muted
		inputStroke.Color = if self.validationError
			then colors.error
			else (if focused then colors.accent else colors.border)
	end)
	self.urlBox = urlBox
	self.inputError = self:_label(connection, "AddressError", "", 13, "error", 3)
	self.inputError.Visible = false
	self.connectButton = self:_button(connection, "Connect", "Connect", 4, function()
		return not self.connected
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
			self:_applyTheme()
			return
		end
		urlBox.Text = url
		plugin:SetSetting(URL_SETTING, url)
		callbacks.onConnect(url)
	end
	self.connectButton.Activated:Connect(function()
		if self.connected then
			callbacks.onDisconnect()
		else
			connect()
		end
	end)
	urlBox.Focused:Connect(function()
		focused = true
		self:_applyTheme()
	end)
	urlBox.FocusLost:Connect(function(enterPressed)
		focused = false
		self:_applyTheme()
		if enterPressed then
			connect()
		end
	end)
	urlBox:GetPropertyChangedSignal("Text"):Connect(function()
		if self.validationError then
			self.validationError = nil
			self.inputError.Visible = false
			self:_applyTheme()
		end
	end)

	local statusCard = stack(root, "Status", 5, 3)
	pad(statusCard, 14)
	round(statusCard, 8)
	statusCard.BackgroundTransparency = 0
	local statusHeading = create("Frame", {
		Name = "Heading",
		Size = UDim2.new(1, 0, 0, 20),
		BackgroundTransparency = 1,
		LayoutOrder = 1,
	}, statusCard)
	self.statusDot = create("Frame", {
		Name = "Indicator",
		Position = UDim2.fromOffset(0, 6),
		Size = UDim2.fromOffset(8, 8),
		BorderSizePixel = 0,
	}, statusHeading)
	round(self.statusDot, 4)
	self.statusLabel = self:_label(statusHeading, "Title", "", 16, "text", 1)
	self.statusLabel.Font = Enum.Font.SourceSansSemibold
	self.statusLabel.Position = UDim2.fromOffset(18, 0)
	self.statusLabel.Size = UDim2.new(1, -18, 0, 0)
	self.statusDetail = self:_label(statusCard, "Detail", "", 14, "muted", 2)
	self:_theme(function(colors)
		local state = STATUS[self.status] or STATUS.disconnected
		local tone = if self.status == "disconnected" and self.detail then "error" else state.tone
		statusCard.BackgroundColor3 = colors.surface
		self.statusDot.BackgroundColor3 = colors[tone]
		self.statusLabel.TextColor3 = if tone == "muted" then colors.text else colors[tone]
	end)

	self.choicePanel = stack(root, "Review", 12, 4)
	self.choicePanel.Visible = false
	local choiceTitle = self:_label(self.choicePanel, "Title", "Choose a starting version", 18, "text", 1)
	choiceTitle.Font = Enum.Font.SourceSansSemibold
	local counts = create("Frame", {
		Name = "Summary",
		Size = UDim2.new(1, 0, 0, 66),
		BackgroundTransparency = 1,
		LayoutOrder = 2,
	}, self.choicePanel)
	self.countLabels = {}
	for index, item in
		{ { "differ", "Properties differ" }, { "diskOnly", "Only on disk" }, { "studioOnly", "Only in Studio" } }
	do
		local cell = create("Frame", {
			Name = item[1],
			Position = UDim2.fromScale((index - 1) / 3, 0),
			Size = UDim2.new(1 / 3, -6, 1, 0),
			BackgroundTransparency = 1,
		}, counts)
		local value = self:_label(cell, "Count", "0", 25, "text", 1)
		value.Font = Enum.Font.SourceSansSemibold
		local caption = self:_label(cell, "Caption", item[2], 12, "muted", 2)
		caption.Position = UDim2.fromOffset(0, 32)
		self.countLabels[item[1]] = value
	end
	local diskChoice = stack(self.choicePanel, "DiskChoice", 5, 3)
	self.keepDisk = self:_button(diskChoice, "KeepDisk", "Keep disk", 1, function()
		return false
	end)
	self:_label(diskChoice, "Description", "Replace synced Studio content with the disk version.", 13, "muted", 2)
	local studioChoice = stack(self.choicePanel, "StudioChoice", 5, 4)
	self.keepStudio = self:_button(studioChoice, "KeepStudio", "Keep Studio", 1, function()
		return false
	end)
	self:_label(studioChoice, "Description", "Write the Studio version to disk.", 13, "muted", 2)
	self.deleteWarning = self:_label(
		self.choicePanel,
		"DeleteWarning",
		"Keeping Studio will delete files that exist only on disk.",
		13,
		"warning",
		5
	)
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
		self:_applyTheme()
		callbacks.onChoose(side)
	end
	self.keepDisk.Activated:Connect(function()
		choose("disk")
	end)
	self.keepStudio.Activated:Connect(function()
		choose("studio")
	end)

	self.help = stack(root, "GettingStarted", 7, 5)
	local helpTitle = self:_label(self.help, "Title", "Start your server", 14, "text", 1)
	helpTitle.Font = Enum.Font.SourceSansSemibold
	self:_label(self.help, "Description", "Run this in your project folder, then connect above.", 13, "muted", 2)
	local command = self:_label(self.help, "Command", "verde serve", 13, "accent", 3)
	command.Font = Enum.Font.Code

	self.themeConnection = settings().Studio.ThemeChanged:Connect(function()
		self:_applyTheme()
	end)
	self:setStatus("disconnected", nil)
	return self
end

function Ui:setStatus(status: string, detail: string?)
	self.status = status
	self.detail = if detail and detail ~= "" then detail else nil
	self.connected = status ~= "disconnected"
	self.connectButton.Text = if status == "connected"
		then "Disconnect"
		else (if self.connected then "Cancel" else "Connect")
	self.urlBox.TextEditable = not self.connected
	self.help.Visible = status == "disconnected"
	local state = STATUS[status] or { title = status, detail = "", tone = "muted" }
	self.statusLabel.Text = if status == "disconnected" and self.detail then "Connection ended" else state.title
	self.statusDetail.Text = self.detail or state.detail
	if status ~= "choosing" then
		self.choicePanel.Visible = false
	end
	self:_applyTheme()
end

function Ui:askChoice(summary: { differ: number, diskOnly: number, studioOnly: number, wouldDeleteFiles: boolean })
	self.choicePending = false
	self.keepDisk.Text = "Keep disk"
	self.keepStudio.Text = "Keep Studio"
	self.keepDisk.Active = true
	self.keepStudio.Active = true
	self.keepDisk.Selectable = true
	self.keepStudio.Selectable = true
	self.countLabels.differ.Text = tostring(summary.differ)
	self.countLabels.diskOnly.Text = tostring(summary.diskOnly)
	self.countLabels.studioOnly.Text = tostring(summary.studioOnly)
	self.deleteWarning.Visible = summary.wouldDeleteFiles
	self.choicePanel.Visible = true
	self:_applyTheme()
end

function Ui:destroy()
	self.themeConnection:Disconnect()
	self.widget:Destroy()
end

return Ui
