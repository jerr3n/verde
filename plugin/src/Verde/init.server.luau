-- SPDX-License-Identifier: MPL-2.0

--[[
	Plugin entry point. The README at the repo root describes how the plugin works.
]]

local RunService = game:GetService("RunService")

-- Plugins also load inside Play sessions, which must never sync.
if not RunService:IsEdit() then
	return
end

local Session = require(script.Session)
local Ui = require(script.Ui)

local TICK_SECONDS = 0.1

local session
local ui = Ui.new(plugin, {
	onConnect = function(url)
		session:connect(url)
	end,
	onDisconnect = function()
		session:disconnect()
	end,
	onChoose = function(side)
		session:choose(side)
	end,
})

session = Session.new({
	onStatus = function(status, detail, title)
		ui:setStatus(status, detail, title)
	end,
	onChoice = function(summary)
		ui:askChoice(summary)
	end,
	onNotice = function(notice)
		ui:notify(notice)
	end,
})

local running = true

plugin.Unloading:Connect(function()
	running = false
	session:disconnect()
	ui:destroy()
end)

local uiFailed = false

while running do
	local now = os.clock()
	session:tick(now)
	-- A display problem must not stop this loop, which is what keeps syncing alive.
	local ok, err = pcall(ui.tick, ui, now, session:info())
	if not ok and not uiFailed then
		uiFailed = true
		warn(`[Verde] the panel stopped updating: {err}`)
	end
	task.wait(TICK_SECONDS)
end
