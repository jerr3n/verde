-- SPDX-License-Identifier: MPL-2.0

--[[
	The activity history behind the panel's feed and its unread badge. It holds no
	instances, so what gets recorded can be tested without any UI.
]]

local Notifications = {}
Notifications.__index = Notifications

export type Kind = "success" | "info" | "warning" | "error" | "activity"

-- Reconnect and Review are the only things a pop-up can offer from its button.
export type Action = "reconnect" | "review"

export type Notice = {
	kind: Kind,
	title: string,
	detail: string?,
	-- Pop-ups with the same key replace each other.
	key: string?,
	-- Updates the newest entry with this key instead of adding a new one.
	merge: boolean?,
	action: Action?,
	-- Stays on screen until it's dismissed or replaced.
	sticky: boolean?,
	-- Recorded in the feed without a pop-up.
	quiet: boolean?,
	-- A normal sync event, which has its own pop-up setting.
	routine: boolean?,
}

export type Entry = Notice & {
	id: number,
	order: number,
	time: number,
	read: boolean,
}

local LIMIT = 100

-- Only these need attention, so only these count as unread.
local NEEDS_ATTENTION = { warning = true, error = true }

function Notifications.new()
	return setmetatable({
		entries = {} :: { Entry },
		nextId = 0,
		nextOrder = 0,
	}, Notifications)
end

function Notifications:_takeOrder(): number
	self.nextOrder += 1
	return self.nextOrder
end

function Notifications:_findLast(key: string): number?
	for index = #self.entries, 1, -1 do
		if self.entries[index].key == key then
			return index
		end
	end
	return nil
end

function Notifications:push(notice: Notice): Entry
	local index = if notice.merge and notice.key then self:_findLast(notice.key) else nil
	if index then
		local entry = table.remove(self.entries, index) :: Entry
		entry.kind = notice.kind
		entry.title = notice.title
		entry.detail = notice.detail
		entry.action = notice.action
		entry.sticky = notice.sticky
		entry.quiet = notice.quiet
		entry.routine = notice.routine
		entry.order = self:_takeOrder()
		entry.time = os.time()
		entry.read = false
		table.insert(self.entries, entry)
		return entry
	end

	self.nextId += 1
	local entry: Entry = table.clone(notice) :: any
	entry.id = self.nextId
	entry.order = self:_takeOrder()
	entry.time = os.time()
	entry.read = false
	table.insert(self.entries, entry)
	if #self.entries > LIMIT then
		table.remove(self.entries, 1)
	end
	return entry
end

-- Entries from oldest to newest.
function Notifications:all(): { Entry }
	return self.entries
end

function Notifications:unread(): number
	local count = 0
	for _, entry in self.entries do
		if not entry.read and NEEDS_ATTENTION[entry.kind] then
			count += 1
		end
	end
	return count
end

function Notifications:markRead()
	for _, entry in self.entries do
		entry.read = true
	end
end

function Notifications:clear()
	table.clear(self.entries)
end

return Notifications
