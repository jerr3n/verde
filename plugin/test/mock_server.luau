--[[
	A stand-in for the Verde backend, for trying the plugin in Studio before the
	real one exists. It speaks PROTOCOL.md with a fixed snapshot and prints whatever
	the plugin sends. Run from plugin/ with:

		lune run test/mock_server.luau [port]

	Then connect the plugin to ws://127.0.0.1:34873/ws. Commands, typed here:

		source <text>     set the Hello script's source
		add <name>        add a ModuleScript to ReplicatedStorage
		rename <name>     rename the Hello script
		remove            remove the Hello script
]]

local net = require("@lune/net")
local process = require("@lune/process")
local serde = require("@lune/serde")
local stdio = require("@lune/stdio")
local task = require("@lune/task")

local port = tonumber(process.args[1]) or 34873
local onConnect = process.env.VERDE_ON_CONNECT or "disk"

local function str(text: string)
	return { t = "String", v = text }
end

local SNAPSHOT = {
	root = "game",
	onConnect = onConnect,
	ignore = { instances = {}, classes = {} },
	mounts = {
		{ id = "game", path = {}, sync = false },
		{ id = "rs", path = { "ReplicatedStorage" }, sync = true },
		{ id = "sss", path = { "ServerScriptService" }, sync = true },
	},
	instances = {
		{ id = "game", name = "game", class = "DataModel" },
		{ id = "rs", parent = "game", name = "ReplicatedStorage", class = "ReplicatedStorage" },
		{ id = "sss", parent = "game", name = "ServerScriptService", class = "ServerScriptService" },
		{ id = "lib", parent = "rs", name = "lib", class = "Folder" },
		{
			id = "hello",
			parent = "lib",
			name = "Hello",
			class = "ModuleScript",
			props = { Source = str('return "hello from the mock server"\n') },
		},
		{
			id = "main",
			parent = "sss",
			name = "Main",
			class = "Script",
			props = { Source = str('print("mock server script")\n') },
		},
	},
}

local socket: any = nil
local seq = 0

local function send(messageType: string, data: any)
	if socket == nil then
		print("(no plugin connected)")
		return
	end
	seq += 1
	socket:send(serde.encode("json", { type = messageType, seq = seq, data = data }))
end

local function handle(message: string)
	local ok, envelope = pcall(serde.decode, "json" :: any, message)
	if not ok then
		print(`<- not JSON: {message}`)
		send("error", { message = "not JSON" })
		return
	end
	print(`<- {message}`)
	if envelope.type == "hello" then
		if envelope.data == nil or envelope.data.protocolVersion ~= 1 then
			send("error", { seq = envelope.seq, message = "unsupported protocol version" })
		else
			send("snapshot", SNAPSHOT)
			print("-> snapshot")
		end
	elseif envelope.type == "patch" then
		send("ack", { seq = envelope.seq })
	end
end

net.serve(port, {
	handleRequest = function()
		return { status = 426, body = "Connect with a WebSocket.\n" }
	end,
	handleWebSocket = function(connected)
		print("plugin connected")
		socket = connected
		seq = 0
		while true do
			local message = connected:next()
			if message == nil then
				break
			end
			handle(message)
		end
		print("plugin disconnected")
		if socket == connected then
			socket = nil
		end
	end,
})

print(`mock server on ws://127.0.0.1:{port}/ws (onConnect = {onConnect})`)

local added = 0
task.spawn(function()
	while true do
		local line = stdio.readLine()
		if line == nil then
			break
		end
		local command, argument = string.match(line, "^(%S+)%s*(.-)%s*$")
		if command == "source" then
			send("patch", { updated = { { id = "hello", props = { Source = str(argument .. "\n") } } } })
		elseif command == "rename" then
			send("patch", { updated = { { id = "hello", name = argument } } })
		elseif command == "remove" then
			send("patch", { removed = { "hello" } })
		elseif command == "add" then
			added += 1
			send("patch", {
				added = {
					{
						id = `added-{added}`,
						parent = "rs",
						name = argument,
						class = "ModuleScript",
						props = { Source = str("return {}\n") },
					},
				},
			})
		elseif command then
			print(`unknown command: {command}`)
		end
	end
end)
