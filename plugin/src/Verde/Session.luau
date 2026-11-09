-- SPDX-License-Identifier: MPL-2.0

--[[
	One connection attempt's lifecycle: connect, say hello, apply the snapshot, then
	exchange patches until the connection ends.

	Everything that changes instances runs from tick, one step at a time. Applying
	a change can yield, so this stops a patch from interleaving with another patch
	or a flush.
]]

local HttpService = game:GetService("HttpService")

local Connection = require(script.Parent.Connection)
local Notifications = require(script.Parent.Notifications)
local Sync = require(script.Parent.Sync)

local PROTOCOL_VERSION = 1
local CLIENT = "verde-plugin/0.1.0"
local SNAPSHOT_TIMEOUT = 5
-- Changes this close together count as one burst and share one notification.
local BURST_SECONDS = 5

local Session = {}
Session.__index = Session

export type Status = "disconnected" | "connecting" | "waiting" | "choosing" | "connected"

export type Summary = { differ: number, diskOnly: number, studioOnly: number, wouldDeleteFiles: boolean }

export type Notice = Notifications.Notice

-- A notice with `dismiss` takes down the pop-up with that key and records nothing.
export type Callbacks = {
	onStatus: (status: Status, detail: string?, title: string?) -> (),
	onChoice: (summary: Summary) -> (),
	onNotice: ((notice: Notice | { dismiss: string }) -> ())?,
}

export type Info = {
	url: string,
	mounts: { string },
	connectedAt: number,
	fromDisk: number,
	toDisk: number,
	lastChangeAt: number?,
}

local function size(list: any): number
	return if type(list) == "table" then #list else 0
end

local function plural(count: number, noun: string, nouns: string?): string
	return if count == 1 then `1 {noun}` else `{count} {nouns or noun .. "s"}`
end

-- "3 changed, 1 added". Parts that are zero are left out.
local function describeCounts(counts: { added: number, changed: number, removed: number }): string
	local parts = {}
	if counts.changed > 0 then
		table.insert(parts, `{counts.changed} changed`)
	end
	if counts.added > 0 then
		table.insert(parts, `{counts.added} added`)
	end
	if counts.removed > 0 then
		table.insert(parts, `{counts.removed} removed`)
	end
	return table.concat(parts, ", ")
end

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
		now = os.clock(),
		url = nil :: string?,
		info_ = nil :: Info?,
		mountPaths = nil :: { string }?,
		bursts = {} :: { [string]: any },
		problems = {} :: { string },
	}, Session)
end

function Session:_notify(notice: any)
	if self.callbacks.onNotice then
		self.callbacks.onNotice(notice)
	end
end

-- Adds counts to the current burst, starting a new one after a quiet spell.
function Session:_burst(name: string, added: number, changed: number, removed: number): (any, boolean)
	local burst = self.bursts[name]
	local continuing = burst ~= nil and self.now - burst.at <= BURST_SECONDS
	if not continuing then
		burst = { added = 0, changed = 0, removed = 0, at = self.now }
		self.bursts[name] = burst
	end
	burst.at = self.now
	burst.added += added
	burst.changed += changed
	burst.removed += removed
	return burst, continuing
end

-- The instance's name when a patch touches exactly one instance.
function Session:_onlyName(patch: any): string?
	local added, updated, removed = size(patch.added), size(patch.updated), size(patch.removed)
	if added + updated + removed ~= 1 then
		return nil
	elseif added == 1 then
		return patch.added[1].name
	elseif updated == 1 and patch.updated[1].name then
		return patch.updated[1].name
	end
	local id = if updated == 1 then patch.updated[1].id else patch.removed[1]
	local instance = self.sync and self.sync.refs[id]
	return if instance then instance.Name else nil
end

function Session:_noteChanges(burstName: string, patch: any, name: string?, verb: string, extra: { [string]: any })
	local added, changed, removed = size(patch.added), size(patch.updated), size(patch.removed)
	if added + changed + removed == 0 then
		return
	end
	if self.info_ then
		local info = self.info_ :: Info
		if burstName == "fromDisk" then
			info.fromDisk += added + changed + removed
		else
			info.toDisk += added + changed + removed
		end
		info.lastChangeAt = self.now
	end
	local burst, continuing = self:_burst(burstName, added, changed, removed)
	local total = burst.added + burst.changed + burst.removed
	local notice = table.clone(extra)
	notice.kind = "activity"
	notice.key = burstName
	notice.merge = continuing
	notice.title = if total == 1 and name
		then `{name} {verb}`
		else string.upper(string.sub(verb, 1, 1)) .. string.sub(verb, 2)
	notice.detail = describeCounts(burst)
	self:_notify(notice)
end

-- Reports what Sync couldn't apply since the last call, as one notification.
function Session:_flushProblems()
	if #self.problems == 0 then
		return
	end
	local burst, continuing = self:_burst("problems", 0, #self.problems, 0)
	self:_notify({
		kind = "warning",
		key = "problems",
		merge = continuing,
		title = `{plural(burst.changed, "change")} couldn't be applied`,
		detail = `{self.problems[#self.problems]}. See Output for details.`,
	})
	table.clear(self.problems)
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
	self.url = url
	self:_notify({ dismiss = "connection" })
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
	local wasConnected = self.status == "connected"
	self:_teardown()
	self:_setStatus("disconnected")
	if wasConnected then
		self:_notify({
			kind = "info",
			key = "connection",
			title = "Disconnected",
			detail = "Studio and disk are no longer syncing.",
		})
	else
		self:_notify({ dismiss = "connection" })
	end
end

-- Live details for the panel while connected, otherwise nil.
function Session:info(): Info?
	return self.info_
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
	self.info_ = nil
	table.clear(self.inbox)
	table.clear(self.bursts)
	table.clear(self.problems)
end

function Session:_end(title: string, detail: string, action: string?)
	self:_teardown()
	self:_setStatus("disconnected", detail, title)
	self:_notify({
		kind = "error",
		key = "connection",
		title = title,
		detail = detail,
		sticky = true,
		action = action,
	})
end

function Session:_fail(detail: string)
	warn(`[Verde] {detail}`)
	self:_end("Couldn't start syncing", detail, "reconnect")
end

function Session:_handleSnapshot(snapshot: any)
	if type(snapshot) ~= "table" or type(snapshot.mounts) ~= "table" or type(snapshot.instances) ~= "table" then
		self:_fail("The server's snapshot has no mounts. Check that the server and plugin are the same version.")
		return
	end

	if self.sync then
		self.sync:stop()
	end
	self.sync = Sync.new(function(patch)
		self:_send("patch", patch)
		self:_noteChanges("toDisk", patch, self:_onlyName(patch), "sent to disk", { quiet = true })
	end, function(message)
		table.insert(self.problems, message)
	end)

	local plan = self.sync:plan(snapshot)
	for _, path in plan.missingMounts do
		warn(`[Verde] {path} doesn't exist in this place, so it isn't being synced`)
	end

	local missing = {}
	for _, path in plan.missingMounts do
		missing[path] = true
	end
	local mounts = {}
	for _, mount in snapshot.mounts do
		local path = table.concat(mount.path, "/")
		if mount.sync and not missing[path] then
			table.insert(mounts, path)
		end
	end
	self.mountPaths = mounts

	local side = snapshot.onConnect or "disk"
	local differs = #plan.differ > 0 or #plan.diskOnly > 0 or #plan.studioOnly > 0
	local mustAsk = (side == "interactive" and differs) or (side == "studio" and #plan.diskOnly > 0)
	if mustAsk then
		self.plan = plan
		self:_setStatus("choosing")
		self:_notify({
			kind = "warning",
			key = "connection",
			title = "Review needed",
			detail = "Disk and Studio differ. Choose which version to keep.",
			sticky = true,
			action = "review",
		})
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
	self.info_ = {
		url = self.url or "",
		mounts = self.mountPaths or {},
		connectedAt = self.now,
		fromDisk = 0,
		toDisk = 0,
		lastChangeAt = nil,
	}
	self.sync:commit(plan, side)

	local missing = #plan.missingMounts
	self:_setStatus("connected", if missing > 0 then `{plural(missing, "folder")} not found in this place.` else nil)

	local detail
	if side == "studio" then
		detail = "Studio's version was sent to disk."
	elseif #plan.diskOnly + #plan.differ == 0 then
		detail = "Studio already matched disk."
	else
		local parts = {}
		if #plan.diskOnly > 0 then
			table.insert(parts, `{plural(#plan.diskOnly, "instance")} added`)
		end
		if #plan.differ > 0 then
			table.insert(parts, `{plural(#plan.differ, "property", "properties")} updated`)
		end
		detail = `Applied from disk: {table.concat(parts, ", ")}.`
	end
	self:_notify({ kind = "success", key = "connection", title = "Connected", detail = detail })

	if missing > 0 then
		self:_notify({
			kind = "warning",
			key = "mounts",
			title = `{plural(missing, "sync folder")} not found`,
			detail = `Not in this place, so not syncing: {table.concat(plan.missingMounts, ", ")}.`,
		})
	end
	if side == "disk" and #plan.studioOnly > 0 then
		self:_notify({
			kind = "warning",
			key = "studio-only",
			title = `{plural(#plan.studioOnly, "instance")} only in Studio`,
			detail = "These aren't being synced. See Output for the list.",
		})
	end
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
			local sync = self.sync
			local patch = envelope.data or {}
			local name = self:_onlyName(patch)
			sync:applyPatch(patch)
			self:_send("ack", { seq = envelope.seq })
			-- Applying can yield, and the user may have disconnected meanwhile.
			if self.sync == sync then
				self:_noteChanges("fromDisk", patch, name, "updated from disk", { routine = true })
			end
		end
	elseif envelope.type == "error" then
		local detail = envelope.data and envelope.data.message or "unknown error"
		if self.sync then
			warn(`[Verde] server error: {detail}`)
			self:_notify({
				kind = "error",
				key = "server-error",
				title = "The server reported a problem",
				detail = `{detail}. Disk and Studio may differ until you reconnect.`,
			})
		else
			self:_fail(`Server error: {detail}`)
		end
	end
end

-- Runs one step. Call repeatedly.
function Session:tick(now: number)
	self.now = now

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
			self:_notify({
				kind = "error",
				key = "apply-failed",
				title = "A change from disk couldn't be applied",
				detail = "Disk and Studio may differ until you reconnect. See Output for details.",
			})
		end
	end

	if self.sync and self.status == "connected" then
		local ok, err = pcall(function()
			self.sync:flush(now)
		end)
		if not ok then
			warn(`[Verde] failed to send changes: {err}`)
			self:_notify({
				kind = "error",
				key = "send-failed",
				title = "Changes couldn't be sent to disk",
				detail = "Disk and Studio may differ until you reconnect. See Output for details.",
			})
		end
	end

	if self.status == "waiting" and self.openedAt and not self.warnedTimeout then
		if now - self.openedAt > SNAPSHOT_TIMEOUT then
			self.warnedTimeout = true
			local detail = "Connected, but the server hasn't sent a snapshot."
			self:_setStatus("waiting", detail)
			self:_notify({
				kind = "warning",
				key = "connection",
				title = "Still waiting for the project",
				detail = detail,
				sticky = true,
			})
		end
	end

	self:_flushProblems()

	-- Handled last so that messages received before the close still apply.
	if self.closeReason and #self.inbox == 0 then
		local reason = self.closeReason.reason
		local wasOpen = self.openedAt ~= nil
		local wasConnected = self.status == "connected"
		if wasOpen then
			self:_end(
				if wasConnected then "Connection lost" else "Connection ended",
				reason or "The server closed the connection.",
				"reconnect"
			)
		else
			self:_end(
				"Couldn't reach the server",
				if reason
					then `Is verde serve running at this address? {reason}`
					else "Is verde serve running at this address?",
				"reconnect"
			)
		end
	end
end

return Session
