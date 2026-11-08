-- SPDX-License-Identifier: MPL-2.0

--[[
	Pop-ups in the corner of the 3D view. They live in CoreGui so they show with the
	panel closed. Studio only draws CoreGui over the 3D view, so they aren't visible
	from a script tab. The panel's title and activity feed cover that case.
]]

local Kit = require(script.Parent.Kit)
local Theme = require(script.Parent.Theme)

local Toasts = {}
Toasts.__index = Toasts

local GUI_NAME = "VerdeNotifications"
local MAX_VISIBLE = 3
local WIDTH = 320
local MARGIN = 16
local DETAIL_LIMIT = 180
-- A pop-up stays this much longer after the mouse leaves it.
local HOVER_GRACE = 2

local DURATIONS = {
	activity = 3,
	info = 4,
	success = 4,
	warning = 8,
	error = 12,
}

local ACTION_LABELS = {
	reconnect = "Reconnect",
	review = "Review",
}

local SLIDE = TweenInfo.new(0.22, Enum.EasingStyle.Quint, Enum.EasingDirection.Out)
local COLLAPSE = TweenInfo.new(0.14, Enum.EasingStyle.Quad, Enum.EasingDirection.Out)

export type Callbacks = {
	-- The user clicked the body of a pop-up.
	onOpen: (entry: any) -> (),
	-- The user clicked a pop-up's action button.
	onAction: (entry: any) -> (),
}

function Toasts.new(theme: any, callbacks: Callbacks)
	local self = setmetatable({
		theme = theme,
		callbacks = callbacks,
		active = {} :: { any },
		order = 0,
		gui = nil :: any,
		holder = nil :: any,
	}, Toasts)

	-- A pop-up failing to show should never get in the way of syncing.
	local ok, err = pcall(function()
		local coreGui = game:GetService("CoreGui")
		-- Left behind if a previous copy of the plugin didn't unload cleanly.
		local stale = coreGui:FindFirstChild(GUI_NAME)
		if stale then
			stale:Destroy()
		end
		self.gui = Kit.create("ScreenGui", {
			Name = GUI_NAME,
			ResetOnSpawn = false,
			ZIndexBehavior = Enum.ZIndexBehavior.Sibling,
			DisplayOrder = 1000,
		}, coreGui)
	end)
	if not ok then
		warn(`[Verde] pop-up notifications are unavailable: {err}`)
		self.gui = nil
		return self
	end

	self.holder = Kit.create("Frame", {
		Name = "Holder",
		AnchorPoint = Vector2.new(1, 1),
		Position = UDim2.new(1, -MARGIN, 1, -MARGIN),
		Size = UDim2.new(1, -MARGIN * 2, 1, -MARGIN * 2),
		BackgroundTransparency = 1,
	}, self.gui)
	Kit.create("UISizeConstraint", { MaxSize = Vector2.new(WIDTH, math.huge) }, self.holder)
	Kit.create("UIListLayout", {
		Padding = UDim.new(0, 8),
		SortOrder = Enum.SortOrder.LayoutOrder,
		VerticalAlignment = Enum.VerticalAlignment.Bottom,
	}, self.holder)

	return self
end

function Toasts:_setContent(toast: any, entry: any)
	toast.entry = entry
	toast.tone = Theme.TONES[entry.kind] or "muted"
	toast.title.Text = entry.title
	toast.detail.Text = Kit.shorten(entry.detail or "", DETAIL_LIMIT)
	toast.detail.Visible = toast.detail.Text ~= ""
	local actionLabel = ACTION_LABELS[entry.action]
	toast.actionButton.Text = actionLabel or ""
	toast.actionButton.Visible = actionLabel ~= nil
	toast.scope:refresh()
	toast.expiresAt = if entry.sticky then nil else os.clock() + (DURATIONS[entry.kind] or 4)
end

function Toasts:_build(entry: any): any
	self.order += 1
	local scope = self.theme:scope()
	local toast: any = { scope = scope, closing = false, hovered = false, tone = "muted" }

	-- Only sized by its card, and collapses to close the gap when dismissed.
	toast.wrapper = Kit.create("Frame", {
		Name = "Toast",
		Size = UDim2.fromScale(1, 0),
		AutomaticSize = Enum.AutomaticSize.Y,
		BackgroundTransparency = 1,
		LayoutOrder = self.order,
	}, self.holder)

	-- A button, so a click lands here and not on whatever is behind it in the 3D view.
	toast.card = Kit.create("TextButton", {
		Name = "Card",
		Position = UDim2.fromOffset(WIDTH + MARGIN, 0),
		Size = UDim2.fromScale(1, 0),
		AutomaticSize = Enum.AutomaticSize.Y,
		AutoButtonColor = false,
		BorderSizePixel = 0,
		Text = "",
	}, toast.wrapper)
	Kit.corner(toast.card, 8)
	local stroke = Kit.create("UIStroke", { ApplyStrokeMode = Enum.ApplyStrokeMode.Border, Thickness = 1 }, toast.card)

	local stripe = Kit.create("Frame", {
		Name = "Tone",
		Position = UDim2.fromOffset(7, 9),
		Size = UDim2.new(0, 3, 1, -18),
		BorderSizePixel = 0,
	}, toast.card)
	Kit.corner(stripe, 2)

	local content = Kit.stack(toast.card, "Content", 3, 1)
	Kit.create("UIPadding", {
		PaddingTop = UDim.new(0, 10),
		PaddingBottom = UDim.new(0, 10),
		PaddingLeft = UDim.new(0, 18),
		PaddingRight = UDim.new(0, 30),
	}, content)
	toast.title = Kit.label(scope, content, "Title", "", { size = 14, font = Kit.BOLD, order = 1 })
	toast.detail = Kit.label(scope, content, "Detail", "", { size = 13, tone = "muted", order = 2 })

	toast.actionButton = Kit.button(scope, content, "Action", "", 3, function()
		return "secondary"
	end)
	toast.actionButton.Size = UDim2.fromOffset(92, 26)
	toast.actionButton.TextSize = 13

	toast.closeButton = Kit.create("TextButton", {
		Name = "Dismiss",
		AnchorPoint = Vector2.new(1, 0),
		Position = UDim2.new(1, -5, 0, 5),
		Size = UDim2.fromOffset(22, 22),
		BackgroundTransparency = 1,
		BorderSizePixel = 0,
		AutoButtonColor = false,
		Font = Kit.MEDIUM,
		TextSize = 18,
		Text = "×",
	}, toast.card)

	scope:bind(function(colors)
		toast.card.BackgroundColor3 = colors.surface
		stroke.Color = colors.border
		stripe.BackgroundColor3 = colors[toast.tone]
		toast.closeButton.TextColor3 = colors.muted
	end)

	toast.card.MouseEnter:Connect(function()
		toast.hovered = true
	end)
	toast.card.MouseLeave:Connect(function()
		toast.hovered = false
	end)
	toast.card.Activated:Connect(function()
		self:_close(toast)
		self.callbacks.onOpen(toast.entry)
	end)
	toast.closeButton.Activated:Connect(function()
		self:_close(toast)
	end)
	toast.actionButton.Activated:Connect(function()
		self:_close(toast)
		self.callbacks.onAction(toast.entry)
	end)

	self:_setContent(toast, entry)
	Kit.tween(toast.card, { Position = UDim2.new() }, SLIDE)
	return toast
end

function Toasts:_close(toast: any)
	if toast.closing then
		return
	end
	toast.closing = true
	local index = table.find(self.active, toast)
	if index then
		table.remove(self.active, index)
	end
	toast.scope:destroy()

	local slide = Kit.tween(toast.card, { Position = UDim2.fromOffset(WIDTH + MARGIN, 0) }, SLIDE)
	slide.Completed:Connect(function(state)
		if state ~= Enum.PlaybackState.Completed then
			return
		end
		local height = toast.wrapper.AbsoluteSize.Y
		toast.wrapper.AutomaticSize = Enum.AutomaticSize.None
		toast.wrapper.Size = UDim2.new(1, 0, 0, height)
		toast.wrapper.ClipsDescendants = true
		local collapse = Kit.tween(toast.wrapper, { Size = UDim2.fromScale(1, 0) }, COLLAPSE)
		collapse.Completed:Connect(function()
			toast.wrapper:Destroy()
		end)
	end)
end

-- Shows an entry, replacing any pop-up with the same key.
function Toasts:show(entry: any)
	if self.gui == nil then
		return
	end

	if entry.key then
		for _, toast in self.active do
			if toast.entry.key == entry.key then
				self:_setContent(toast, entry)
				return
			end
		end
	end

	if #self.active >= MAX_VISIBLE then
		-- Oldest first, but anything still waiting on the user goes last.
		local victim = self.active[1]
		for _, toast in self.active do
			if not toast.entry.sticky then
				victim = toast
				break
			end
		end
		self:_close(victim)
	end
	table.insert(self.active, self:_build(entry))
end

function Toasts:dismiss(key: string)
	for _, toast in table.clone(self.active) do
		if toast.entry.key == key then
			self:_close(toast)
		end
	end
end

function Toasts:dismissAll()
	for _, toast in table.clone(self.active) do
		self:_close(toast)
	end
end

function Toasts:tick(now: number)
	for _, toast in table.clone(self.active) do
		if toast.expiresAt then
			if toast.hovered then
				toast.expiresAt = math.max(toast.expiresAt, now + HOVER_GRACE)
			elseif now >= toast.expiresAt then
				self:_close(toast)
			end
		end
	end
end

function Toasts:destroy()
	for _, toast in self.active do
		toast.scope:destroy()
	end
	table.clear(self.active)
	if self.gui then
		self.gui:Destroy()
		self.gui = nil
	end
end

return Toasts
