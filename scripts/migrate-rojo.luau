-- Run with: lune run scripts/migrate-rojo.luau path/to/default.project.json
local fs = require("@lune/fs")
local process = require("@lune/process")
local roblox = require("@lune/roblox")
local serde = require("@lune/serde")
local stdio = require("@lune/stdio")
local Migration = require("./lib/migrate-rojo")

local usage = [[Usage: lune run scripts/migrate-rojo.luau [PROJECT] [OPTIONS]

Build a Rojo place and write a converted Verde project in a new folder.
PROJECT defaults to default.project.json (or .jsonc) in the current folder.
A project directory can also be supplied. Requires Rojo and Lune 0.10.4+.

  -o, --output DIR  Destination (default: verde/ beside the Rojo project)
      --dry-run     Build and report without creating the destination
      --rojo PATH   Rojo executable to use (default: rojo)
  -h, --help        Show this help

The destination must not exist. Original source files are left intact.
]]

local function absolute(path)
	if string.sub(path, 1, 1) == "/" or string.match(path, "^%a:[/\\]") or string.sub(path, 1, 2) == "\\\\" then
		return path
	end
	return `{string.gsub(process.cwd, "[/\\]+$", "")}/{path}`
end

local function dirname(path)
	local parent = string.match(path, "^(.*)[/\\][^/\\]+$")
	if parent == "" then
		return "/"
	end
	if parent and string.match(parent, "^%a:$") then
		return parent .. "/"
	end
	return parent or "."
end

local function exists(path)
	return fs.metadata(path).exists
end

local staging = nil
local function main()
	local projectPath, output = nil, nil
	local dryRun = false
	local rojo = "rojo"
	local index = 1
	while index <= #process.args do
		local arg = process.args[index]
		if arg == "--help" or arg == "-h" then
			print(usage)
			return
		elseif arg == "--output" or arg == "-o" or arg == "--rojo" then
			index += 1
			local value = process.args[index]
			assert(value and string.sub(value, 1, 1) ~= "-", `{arg} needs a value`)
			if arg == "--rojo" then
				rojo = value
			else
				output = value
			end
		elseif arg == "--dry-run" then
			dryRun = true
		elseif string.sub(arg, 1, 1) == "-" then
			error(`unknown option {arg}; use --help`, 0)
		else
			assert(not projectPath, "only one Rojo project can be supplied")
			projectPath = arg
		end
		index += 1
	end

	projectPath = absolute(projectPath or ".")
	if fs.isDir(projectPath) then
		if fs.isFile(`{projectPath}/default.project.json`) then
			projectPath ..= "/default.project.json"
		else
			projectPath ..= "/default.project.jsonc"
		end
	end
	assert(fs.isFile(projectPath), `Rojo project not found: {projectPath}`)
	local project = serde.decode("jsonc", fs.readFile(projectPath))
	assert(type(project) == "table" and type(project.tree) == "table", "expected a Rojo project with a tree")
	assert(
		project.tree["$className"] == "DataModel",
		"expected a Rojo place project (tree.$className must be DataModel)"
	)
	output = string.gsub(absolute(output or `{dirname(projectPath)}/verde`), "[/\\]+$", "")
	assert(output ~= "", "destination cannot be the filesystem root")
	assert(not exists(output), `destination already exists: {output}; choose a new folder with --output`)
	assert(fs.isDir(dirname(output)), `destination's parent folder does not exist: {dirname(output)}`)
	-- The random suffix avoids sharing a work folder with another migration. Move
	-- refuses to replace an output folder created while conversion was running.
	local token = serde.hash("sha256", `{os.clock()}:{tostring({})}`)
	local workDir = `{output}.migrating-{string.sub(token, 1, 16)}`
	assert(not exists(workDir), `temporary folder already exists: {workDir}`)
	fs.writeDir(workDir)
	staging = workDir
	local snapshot = `{staging}/rojo-snapshot.rbxlx`
	if string.find(rojo, "/", 1, true) or string.find(rojo, "\\", 1, true) then
		rojo = absolute(rojo)
	end
	local built = process.exec(rojo, { "build", projectPath, "--output", snapshot, "--color", "never" }, {
		cwd = dirname(projectPath),
	})
	assert(built.ok, `Rojo build failed (exit {built.code}):\n{built.stderr}\n{built.stdout}`)
	fs.writeFile(`{staging}/rojo-build.log`, built.stdout .. built.stderr)
	if built.stderr ~= "" then
		stdio.ewrite(built.stderr)
	end
	local game = roblox.deserializePlace(fs.readFile(snapshot))
	local result = Migration.convert(game, project, staging)
	for _, warning in result.warnings do
		stdio.ewrite(`warning: {warning}\n`)
	end
	if dryRun then
		print(`Dry run: would migrate {result.instances} instances into {result.mounts} sync targets at {output}.`)
		print(`{#result.warnings} warning(s). Original files and destination are unchanged.`)
	else
		fs.move(staging, output, false)
		staging = nil
		print(`Migrated {result.instances} instances into {result.mounts} sync targets at {output}.`)
		print(`{#result.warnings} warning(s). Read {output}/migration-report.txt before connecting.`)
		print(`Open that folder, run verde serve, then connect the Verde Studio plugin to ws://127.0.0.1:45678/ws.`)
	end
end

local ok, err = pcall(main)
if staging and fs.isDir(staging) then
	fs.removeDir(staging)
end
if not ok then
	stdio.ewrite(`error: {err}\n`)
	process.exit(1)
end
