--[[
	Plugin entry point. PROTOCOL.md describes what the server has to do, and
	README.md describes how the plugin works.
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
	onStatus = function(status, detail)
		ui:setStatus(status, detail)
	end,
	onChoice = function(summary)
		ui:askChoice(summary)
	end,
})

local running = true

plugin.Unloading:Connect(function()
	running = false
	session:disconnect()
	ui:destroy()
end)

while running do
	session:tick(os.clock())
	task.wait(TICK_SECONDS)
end
