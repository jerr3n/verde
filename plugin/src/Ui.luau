--[[
	The dock widget: a server URL, a connect button, a status line, and the
	question shown when a person has to pick which side wins.
]]

local Ui = {}
Ui.__index = Ui

local WIDGET_ID = "VerdeWidget"
local URL_SETTING = "VerdeServerUrl"
local DEFAULT_URL = "ws://127.0.0.1:34873/ws"

local STATUS_TEXT = {
	disconnected = "Disconnected",
	connecting = "Connecting...",
	waiting = "Waiting for project...",
	choosing = "Choose which side to keep",
	connected = "Connected",
}

export type Callbacks = {
	onConnect: (url: string) -> (),
	onDisconnect: () -> (),
	onChoose: (side: string) -> (),
}

local function studioColor(guide: Enum.StudioStyleGuideColor, modifier: Enum.StudioStyleGuideModifier?): Color3
	return settings().Studio.Theme:GetColor(guide, modifier)
end

function Ui.new(plugin: Plugin, callbacks: Callbacks)
	local self = setmetatable({ connected = false, themed = {} :: { () -> () } }, Ui)

	local toolbar = plugin:CreateToolbar("Verde")
	local toggle = toolbar:CreateButton("Verde", "Show or hide the Verde sync panel", "")
	toggle.ClickableWhenViewportHidden = true

	local widget = plugin:CreateDockWidgetPluginGuiAsync(
		WIDGET_ID,
		DockWidgetPluginGuiInfo.new(Enum.InitialDockState.Float, true, false, 320, 170, 240, 150)
	)
	widget.Title = "Verde"
	widget.Name = WIDGET_ID
	toggle:SetActive(widget.Enabled)
	toggle.Click:Connect(function()
		widget.Enabled = not widget.Enabled
	end)
	widget:GetPropertyChangedSignal("Enabled"):Connect(function()
		toggle:SetActive(widget.Enabled)
	end)

	local root = Instance.new("Frame")
	root.Size = UDim2.fromScale(1, 1)
	root.BorderSizePixel = 0
	root.Parent = widget
	self:_theme(function()
		root.BackgroundColor3 = studioColor(Enum.StudioStyleGuideColor.MainBackground)
	end)

	local padding = Instance.new("UIPadding")
	padding.PaddingTop = UDim.new(0, 8)
	padding.PaddingBottom = UDim.new(0, 8)
	padding.PaddingLeft = UDim.new(0, 8)
	padding.PaddingRight = UDim.new(0, 8)
	padding.Parent = root

	local layout = Instance.new("UIListLayout")
	layout.Padding = UDim.new(0, 6)
	layout.SortOrder = Enum.SortOrder.LayoutOrder
	layout.Parent = root

	local order = 0
	local function place(element: GuiObject)
		order += 1
		element.LayoutOrder = order
		element.Parent = root
	end

	local urlBox = Instance.new("TextBox")
	urlBox.Size = UDim2.new(1, 0, 0, 26)
	urlBox.ClearTextOnFocus = false
	urlBox.Font = Enum.Font.Code
	urlBox.TextSize = 13
	urlBox.TextXAlignment = Enum.TextXAlignment.Left
	urlBox.Text = plugin:GetSetting(URL_SETTING) or DEFAULT_URL
	place(urlBox)
	self:_theme(function()
		urlBox.BackgroundColor3 = studioColor(Enum.StudioStyleGuideColor.InputFieldBackground)
		urlBox.BorderColor3 = studioColor(Enum.StudioStyleGuideColor.InputFieldBorder)
		urlBox.TextColor3 = studioColor(Enum.StudioStyleGuideColor.MainText)
	end)

	local function makeButton(text: string): TextButton
		local button = Instance.new("TextButton")
		button.Size = UDim2.new(1, 0, 0, 26)
		button.Font = Enum.Font.SourceSans
		button.TextSize = 14
		button.Text = text
		self:_theme(function()
			button.BackgroundColor3 = studioColor(Enum.StudioStyleGuideColor.Button)
			button.BorderColor3 = studioColor(Enum.StudioStyleGuideColor.ButtonBorder)
			button.TextColor3 = studioColor(Enum.StudioStyleGuideColor.ButtonText)
		end)
		return button
	end

	self.connectButton = makeButton("Connect")
	place(self.connectButton)
	self.connectButton.Activated:Connect(function()
		if self.connected then
			callbacks.onDisconnect()
		else
			plugin:SetSetting(URL_SETTING, urlBox.Text)
			callbacks.onConnect(urlBox.Text)
		end
	end)

	local function makeLabel(): TextLabel
		local label = Instance.new("TextLabel")
		label.Size = UDim2.fromScale(1, 0)
		label.AutomaticSize = Enum.AutomaticSize.Y
		label.BackgroundTransparency = 1
		label.Font = Enum.Font.SourceSans
		label.TextSize = 14
		label.TextWrapped = true
		label.TextXAlignment = Enum.TextXAlignment.Left
		self:_theme(function()
			label.TextColor3 = studioColor(Enum.StudioStyleGuideColor.MainText)
		end)
		return label
	end

	self.statusLabel = makeLabel()
	place(self.statusLabel)

	self.questionLabel = makeLabel()
	self.questionLabel.Visible = false
	place(self.questionLabel)

	self.keepDisk = makeButton("Keep disk")
	self.keepDisk.Visible = false
	place(self.keepDisk)
	self.keepDisk.Activated:Connect(function()
		callbacks.onChoose("disk")
	end)

	self.keepStudio = makeButton("Keep Studio")
	self.keepStudio.Visible = false
	place(self.keepStudio)
	self.keepStudio.Activated:Connect(function()
		callbacks.onChoose("studio")
	end)

	self.urlBox = urlBox
	self:_applyTheme()
	self.themeConnection = settings().Studio.ThemeChanged:Connect(function()
		self:_applyTheme()
	end)
	self:setStatus("disconnected", nil)

	return self
end

function Ui:_theme(apply: () -> ())
	table.insert(self.themed, apply)
end

function Ui:_applyTheme()
	for _, apply in self.themed do
		apply()
	end
end

function Ui:setStatus(status: string, detail: string?)
	self.connected = status ~= "disconnected"
	self.connectButton.Text = if self.connected then "Disconnect" else "Connect"
	self.urlBox.TextEditable = not self.connected

	local text = STATUS_TEXT[status] or status
	self.statusLabel.Text = if detail then `{text}\n{detail}` else text

	if status ~= "choosing" then
		self.questionLabel.Visible = false
		self.keepDisk.Visible = false
		self.keepStudio.Visible = false
	end
end

function Ui:askChoice(summary: { differ: number, diskOnly: number, studioOnly: number, wouldDeleteFiles: boolean })
	local text =
		`{summary.differ} properties differ\n{summary.diskOnly} only on disk\n{summary.studioOnly} only in Studio`
	if summary.wouldDeleteFiles then
		text ..= "\nKeeping Studio deletes what's only on disk."
	end
	self.questionLabel.Text = text
	self.questionLabel.Visible = true
	self.keepDisk.Visible = true
	self.keepStudio.Visible = true
end

function Ui:destroy()
	self.themeConnection:Disconnect()
end

return Ui
