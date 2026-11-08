-- SPDX-License-Identifier: MPL-2.0

--[[
	Colors that follow Studio's theme. Anything bound here repaints when the theme
	changes. Short-lived UI, such as a pop-up, binds to a scope and destroys it
	when it goes away, so its callbacks don't pile up.
]]

local Theme = {}
Theme.__index = Theme

export type Colors = { [string]: Color3 }

-- What each notification kind is colored with.
Theme.TONES = {
	success = "accent",
	info = "info",
	warning = "warning",
	error = "error",
	activity = "muted",
}

local function palette(): Colors
	local studio = settings().Studio.Theme
	local background = studio:GetColor(Enum.StudioStyleGuideColor.MainBackground)
	local dark = background.R + background.G + background.B < 1.5
	local ink = studio:GetColor(Enum.StudioStyleGuideColor.MainText)
	return {
		background = background,
		surface = background:Lerp(ink, if dark then 0.045 else 0.03),
		raised = background:Lerp(ink, if dark then 0.09 else 0.06),
		input = studio:GetColor(Enum.StudioStyleGuideColor.InputFieldBackground),
		border = background:Lerp(ink, if dark then 0.16 else 0.18),
		text = ink,
		muted = background:Lerp(ink, if dark then 0.61 else 0.67),
		accent = if dark then Color3.fromRGB(77, 206, 132) else Color3.fromRGB(20, 121, 67),
		primary = Color3.fromRGB(26, 132, 76),
		primaryHover = Color3.fromRGB(22, 115, 65),
		onPrimary = Color3.new(1, 1, 1),
		info = if dark then Color3.fromRGB(124, 180, 248) else Color3.fromRGB(32, 98, 184),
		warning = if dark then Color3.fromRGB(238, 187, 91) else Color3.fromRGB(145, 93, 15),
		error = if dark then Color3.fromRGB(247, 137, 137) else Color3.fromRGB(183, 48, 48),
	}
end

function Theme.new()
	local self = setmetatable({
		colors = palette(),
		bindings = {} :: { (colors: Colors) -> () },
		scopes = {} :: { [any]: boolean },
		parent = nil :: any,
		connection = nil :: any,
	}, Theme)
	self.connection = settings().Studio.ThemeChanged:Connect(function()
		self.colors = palette()
		self:refresh()
	end)
	return self
end

-- Calls apply now and again whenever the colors or the UI's state change.
function Theme:bind(apply: (colors: Colors) -> ())
	table.insert(self.bindings, apply)
	apply(self.colors)
end

function Theme:refresh()
	for _, apply in self.bindings do
		apply(self.colors)
	end
	for scope in self.scopes do
		scope.colors = self.colors
		scope:refresh()
	end
end

function Theme:scope()
	local scope = setmetatable({
		colors = self.colors,
		bindings = {},
		scopes = {},
		parent = self,
		connection = nil,
	}, Theme)
	self.scopes[scope] = true
	return scope
end

function Theme:destroy()
	if self.connection then
		self.connection:Disconnect()
		self.connection = nil
	end
	if self.parent then
		self.parent.scopes[self] = nil
	end
	table.clear(self.bindings)
	table.clear(self.scopes)
end

return Theme
