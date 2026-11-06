--[[
	The small set of building blocks the panel and the pop-ups share.
]]

local TweenService = game:GetService("TweenService")

local Kit = {}

Kit.REGULAR = Enum.Font.BuilderSans
Kit.MEDIUM = Enum.Font.BuilderSansMedium
Kit.BOLD = Enum.Font.BuilderSansBold
Kit.MONO = Enum.Font.RobotoMono

-- The logo is uploaded as a Decal, and only an Image ID works with rbxassetid://.
-- The thumbnail service serves a Decal's picture from its own ID.
Kit.LOGO = "rbxthumb://type=Asset&id=107008526393062&w=150&h=150"

local QUICK = TweenInfo.new(0.16, Enum.EasingStyle.Quad, Enum.EasingDirection.Out)

function Kit.create(className: string, properties: { [string]: any }, parent: Instance?): any
	local object = Instance.new(className)
	for key, value in properties do
		(object :: any)[key] = value
	end
	object.Parent = parent
	return object
end

function Kit.tween(object: Instance, goal: { [string]: any }, info: TweenInfo?): Tween
	local tween = TweenService:Create(object, info or QUICK, goal)
	tween:Play()
	return tween
end

function Kit.corner(object: Instance, radius: number)
	Kit.create("UICorner", { CornerRadius = UDim.new(0, radius) }, object)
end

function Kit.padding(object: Instance, vertical: number, horizontal: number?)
	Kit.create("UIPadding", {
		PaddingTop = UDim.new(0, vertical),
		PaddingBottom = UDim.new(0, vertical),
		PaddingLeft = UDim.new(0, horizontal or vertical),
		PaddingRight = UDim.new(0, horizontal or vertical),
	}, object)
end

-- A transparent frame that grows to fit children listed top to bottom.
function Kit.stack(parent: Instance, name: string, gap: number, order: number): any
	local frame = Kit.create("Frame", {
		Name = name,
		Size = UDim2.fromScale(1, 0),
		AutomaticSize = Enum.AutomaticSize.Y,
		BackgroundTransparency = 1,
		BorderSizePixel = 0,
		LayoutOrder = order,
	}, parent)
	Kit.create("UIListLayout", {
		Padding = UDim.new(0, gap),
		SortOrder = Enum.SortOrder.LayoutOrder,
	}, frame)
	return frame
end

-- A stack on a rounded, outlined background.
function Kit.card(theme: any, parent: Instance, name: string, gap: number, order: number): any
	local frame = Kit.stack(parent, name, gap, order)
	frame.BackgroundTransparency = 0
	Kit.corner(frame, 8)
	Kit.padding(frame, 12)
	local stroke = Kit.create("UIStroke", { ApplyStrokeMode = Enum.ApplyStrokeMode.Border, Thickness = 1 }, frame)
	theme:bind(function(colors)
		frame.BackgroundColor3 = colors.surface
		stroke.Color = colors.border
	end)
	return frame
end

export type LabelOptions = { size: number?, tone: string?, font: Enum.Font?, order: number? }

function Kit.label(theme: any, parent: Instance, name: string, text: string, options: LabelOptions?): any
	local settings_ = options or {} :: LabelOptions
	local tone = settings_.tone or "text"
	local label = Kit.create("TextLabel", {
		Name = name,
		Size = UDim2.fromScale(1, 0),
		AutomaticSize = Enum.AutomaticSize.Y,
		BackgroundTransparency = 1,
		Font = settings_.font or Kit.REGULAR,
		TextSize = settings_.size or 14,
		Text = text,
		TextWrapped = true,
		TextXAlignment = Enum.TextXAlignment.Left,
		TextYAlignment = Enum.TextYAlignment.Top,
		LayoutOrder = settings_.order or 0,
	}, parent)
	theme:bind(function(colors)
		label.TextColor3 = colors[tone]
	end)
	return label
end

-- variant returns "primary", "secondary" or "danger". A button with Active false
-- is painted as disabled.
function Kit.button(theme: any, parent: Instance, name: string, text: string, order: number, variant: () -> string): any
	local button = Kit.create("TextButton", {
		Name = name,
		Size = UDim2.new(1, 0, 0, 34),
		BorderSizePixel = 0,
		AutoButtonColor = false,
		Font = Kit.MEDIUM,
		TextSize = 14,
		Text = text,
		LayoutOrder = order,
	}, parent)
	Kit.corner(button, 6)
	local stroke = Kit.create("UIStroke", { ApplyStrokeMode = Enum.ApplyStrokeMode.Border, Thickness = 1 }, button)
	local hovered = false
	local function paint(colors: { [string]: Color3 })
		local disabled = not button.Active
		local kind = variant()
		if disabled then
			button.BackgroundColor3 = colors.surface
			button.TextColor3 = colors.muted
			stroke.Color = colors.border
		elseif kind == "primary" then
			button.BackgroundColor3 = if hovered then colors.primaryHover else colors.primary
			button.TextColor3 = colors.onPrimary
			stroke.Color = if hovered then colors.primaryHover else colors.primary
		else
			button.BackgroundColor3 = if hovered then colors.raised else colors.surface
			button.TextColor3 = if kind == "danger" then colors.error else colors.text
			stroke.Color = if kind == "danger" then colors.error else colors.border
		end
	end
	theme:bind(paint)
	button.MouseEnter:Connect(function()
		hovered = true
		paint(theme.colors)
	end)
	button.MouseLeave:Connect(function()
		hovered = false
		paint(theme.colors)
	end)
	return button
end

-- Shortens text for places with no room to scroll. Never splits a character.
function Kit.shorten(text: string, limit: number): string
	local cut = utf8.offset(text, limit + 1)
	return if cut and cut <= #text then string.sub(text, 1, cut - 1) .. "…" else text
end

return Kit
