--[[
	One connection attempt's lifecycle: connect, say hello, apply the snapshot, then
	exchange patches until the connection ends.

	Everything that changes instances runs from tick, one step at a time. Applying
	a change can yield, so this stops a patch from interleaving with another patch
	or a flush.
]]

local HttpService = game:GetService("HttpService")

local Connection = require(script.Parent.Connection)
local Sync = require(script.Parent.Sync)

local PROTOCOL_VERSION = 1
local CLIENT = "verde-plugin/0.1.0"
local SNAPSHOT_TIMEOUT = 5

local Session = {}
Session.__index = Session

export type Status = "disconnected" | "connecting" | "waiting" | "choosing" | "connected"

export type Summary = { differ: number, diskOnly: number, studioOnly: number, wouldDeleteFiles: boolean }

export type Callbacks = {
	onStatus: (status: Status, detail: string?) -> (),
	onChoice: (summary: Summary) -> (),
}

function Session.new(callbacks: Callbacks, connect: any?)
	return setmetatable({
		callbacks = callbacks,
		connectTo = connect or Connection.new,
		status = "disconnected" :: Status,
		connection = nil :: any,
		sync = nil :: any,
		plan = nil :: any,
		chosenSide = nil :: string?,
		inbox = {} :: { string },
		seq = 0,
		openedAt = nil :: number?,
		warnedTimeout = false,
		closeReason = nil :: { reason: string? }?,
	}, Session)
end

function Session:_setStatus(status: Status, detail: string?)
	self.status = status
	self.callbacks.onStatus(status, detail)
end

function Session:_send(messageType: string, data: any)
	if self.connection then
		self.seq += 1
		self.connection:send(HttpService:JSONEncode({ type = messageType, seq = self.seq, data = data }))
	end
end

function Session:connect(url: string)
	self:_teardown()
	self:_setStatus("connecting")
	self.connection = self.connectTo(url, {
		onOpen = function()
			self.openedAt = os.clock()
			self:_setStatus("waiting")
			self:_send("hello", { protocolVersion = PROTOCOL_VERSION, client = CLIENT, placeId = game.PlaceId })
		end,
		onMessage = function(message)
			table.insert(self.inbox, message)
		end,
		onClosed = function(reason)
			self.closeReason = { reason = reason }
		end,
	})
end

function Session:disconnect()
	self:_teardown()
	self:_setStatus("disconnected")
end

-- Records the user's answer to onChoice. "disk" or "studio"; tick applies it.
function Session:choose(side: string)
	if self.status == "choosing" then
		self.chosenSide = side
	end
end

function Session:_teardown()
	if self.connection then
		self.connection:close()
		self.connection = nil
	end
	if self.sync then
		self.sync:stop()
		self.sync = nil
	end
	self.plan = nil
	self.chosenSide = nil
	self.seq = 0
	self.openedAt = nil
	self.warnedTimeout = false
	self.closeReason = nil
	table.clear(self.inbox)
end

function Session:_fail(detail: string)
	warn(`[Verde] {detail}`)
	self:_teardown()
	self:_setStatus("disconnected", detail)
end

function Session:_handleSnapshot(snapshot: any)
	if type(snapshot) ~= "table" or type(snapshot.mounts) ~= "table" or type(snapshot.instances) ~= "table" then
		self:_fail("The server's snapshot has no mounts. See plugin/PROTOCOL.md.")
		return
	end

	if self.sync then
		self.sync:stop()
	end
	self.sync = Sync.new(function(patch)
		self:_send("patch", patch)
	end)

	local plan = self.sync:plan(snapshot)
	for _, path in plan.missingMounts do
		warn(`[Verde] {path} doesn't exist in this place, so it isn't being synced`)
	end

	local side = snapshot.onConnect or "disk"
	local differs = #plan.differ > 0 or #plan.diskOnly > 0 or #plan.studioOnly > 0
	local mustAsk = (side == "interactive" and differs) or (side == "studio" and #plan.diskOnly > 0)
	if mustAsk then
		self.plan = plan
		self:_setStatus("choosing")
		self.callbacks.onChoice({
			differ = #plan.differ,
			diskOnly = #plan.diskOnly,
			studioOnly = #plan.studioOnly,
			wouldDeleteFiles = #plan.diskOnly > 0,
		})
	else
		self:_commit(plan, if side == "studio" then "studio" else "disk")
	end
end

function Session:_commit(plan: any, side: string)
	self.sync:commit(plan, side)
	self:_setStatus("connected", #plan.missingMounts > 0 and "Some mounts weren't found. See Output." or nil)
end

function Session:_handleMessage(message: string)
	local ok, envelope = pcall(function()
		return HttpService:JSONDecode(message)
	end)
	if not ok or type(envelope) ~= "table" then
		warn("[Verde] ignoring a message that isn't JSON")
		return
	end

	if envelope.type == "snapshot" then
		self:_handleSnapshot(envelope.data)
	elseif envelope.type == "patch" then
		if self.sync and self.status == "connected" then
			self.sync:applyPatch(envelope.data or {})
			self:_send("ack", { seq = envelope.seq })
		end
	elseif envelope.type == "error" then
		local detail = envelope.data and envelope.data.message or "unknown error"
		if self.sync then
			warn(`[Verde] server error: {detail}`)
		else
			self:_fail(`Server error: {detail}`)
		end
	end
end

-- Runs one step. Call repeatedly.
function Session:tick(now: number)
	if self.chosenSide and self.plan then
		local plan, side = self.plan, self.chosenSide
		self.plan = nil
		self.chosenSide = nil
		self:_commit(plan, side)
	end

	-- While the user is choosing, messages wait so they apply to the final state.
	while #self.inbox > 0 and self.status ~= "choosing" do
		local message = table.remove(self.inbox, 1) :: string
		local ok, err = pcall(function()
			self:_handleMessage(message)
		end)
		if not ok then
			warn(`[Verde] failed to apply a message: {err}`)
		end
	end

	if self.sync and self.status == "connected" then
		local ok, err = pcall(function()
			self.sync:flush(now)
		end)
		if not ok then
			warn(`[Verde] failed to send changes: {err}`)
		end
	end

	if self.status == "waiting" and self.openedAt and not self.warnedTimeout then
		if now - self.openedAt > SNAPSHOT_TIMEOUT then
			self.warnedTimeout = true
			self:_setStatus("waiting", "Connected, but the server hasn't sent a snapshot.")
		end
	end

	-- Handled last so that messages received before the close still apply.
	if self.closeReason and #self.inbox == 0 then
		local reason = self.closeReason.reason
		self:_teardown()
		self:_setStatus("disconnected", reason or "The server closed the connection.")
	end
end

return Session
