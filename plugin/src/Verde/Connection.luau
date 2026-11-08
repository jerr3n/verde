-- SPDX-License-Identifier: MPL-2.0

--[[
	A WebSocket to the Verde server. Callbacks fire at most once for onClosed, and
	never after close is called.
]]

local HttpService = game:GetService("HttpService")

local Connection = {}
Connection.__index = Connection

export type Callbacks = {
	onOpen: () -> (),
	onMessage: (string) -> (),
	onClosed: (string?) -> (),
}

function Connection.new(url: string, callbacks: Callbacks)
	local self = setmetatable({ callbacks = callbacks, closed = false, client = nil :: any }, Connection)

	-- The cast is needed until the type definitions list WebSocket.
	local ok, client = pcall(function()
		return HttpService:CreateWebStreamClient((Enum.WebStreamClientType :: any).WebSocket, { Url = url })
	end)
	if not ok then
		task.defer(function()
			self:_finish(tostring(client))
		end)
		return self
	end
	self.client = client

	client.Opened:Connect(function()
		if not self.closed then
			callbacks.onOpen()
		end
	end)
	client.MessageReceived:Connect(function(message)
		if not self.closed then
			callbacks.onMessage(message)
		end
	end)
	client.Error:Connect(function(status, message)
		self:_finish(`{message} (status {status})`)
	end)
	client.Closed:Connect(function()
		self:_finish(nil)
	end)

	return self
end

function Connection:_finish(reason: string?)
	if self.closed then
		return
	end
	self.closed = true
	self:_closeClient()
	self.callbacks.onClosed(reason)
end

-- Studio allows only a few clients at once, so each one has to be closed.
function Connection:_closeClient()
	if self.client then
		pcall(function()
			self.client:Close()
		end)
		self.client = nil
	end
end

function Connection:send(message: string): boolean
	if self.closed or self.client == nil then
		return false
	end
	local ok, err = pcall(function()
		self.client:Send(message)
	end)
	if not ok then
		self:_finish(tostring(err))
	end
	return ok
end

function Connection:close()
	self.closed = true
	self:_closeClient()
end

return Connection
