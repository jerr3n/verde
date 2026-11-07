--[[
	Converts Roblox values to and from the wire format { t = type, v = value }.
	PROTOCOL.md lists every type.
]]

local HttpService = game:GetService("HttpService")

local Values = {}

export type Wire = { t: string, v: any? }

-- Keyed by typeof() name, which is also the wire type name.
local codecs: { [string]: { encode: (any) -> any, decode: (any) -> any } } = {
	Vector2 = {
		encode = function(x)
			return { x.X, x.Y }
		end,
		decode = function(v)
			return Vector2.new(v[1], v[2])
		end,
	},
	Vector3 = {
		encode = function(x)
			return { x.X, x.Y, x.Z }
		end,
		decode = function(v)
			return Vector3.new(v[1], v[2], v[3])
		end,
	},
	CFrame = {
		encode = function(x)
			return { x:GetComponents() }
		end,
		decode = function(v)
			return CFrame.new(table.unpack(v, 1, 12))
		end,
	},
	Color3 = {
		encode = function(x)
			return { x.R, x.G, x.B }
		end,
		decode = function(v)
			return Color3.new(v[1], v[2], v[3])
		end,
	},
	BrickColor = {
		encode = function(x)
			return x.Number
		end,
		decode = function(v)
			return BrickColor.new(v)
		end,
	},
	UDim = {
		encode = function(x)
			return { x.Scale, x.Offset }
		end,
		decode = function(v)
			return UDim.new(v[1], v[2])
		end,
	},
	UDim2 = {
		encode = function(x)
			return { x.X.Scale, x.X.Offset, x.Y.Scale, x.Y.Offset }
		end,
		decode = function(v)
			return UDim2.new(v[1], v[2], v[3], v[4])
		end,
	},
	Rect = {
		encode = function(x)
			return { x.Min.X, x.Min.Y, x.Max.X, x.Max.Y }
		end,
		decode = function(v)
			return Rect.new(v[1], v[2], v[3], v[4])
		end,
	},
	NumberRange = {
		encode = function(x)
			return { x.Min, x.Max }
		end,
		decode = function(v)
			return NumberRange.new(v[1], v[2])
		end,
	},
	NumberSequence = {
		encode = function(x)
			local out = {}
			for i, k in x.Keypoints do
				out[i] = { k.Time, k.Value, k.Envelope }
			end
			return out
		end,
		decode = function(v)
			local keypoints = {}
			for i, k in v do
				keypoints[i] = NumberSequenceKeypoint.new(k[1], k[2], k[3])
			end
			return NumberSequence.new(keypoints)
		end,
	},
	ColorSequence = {
		encode = function(x)
			local out = {}
			for i, k in x.Keypoints do
				out[i] = { k.Time, k.Value.R, k.Value.G, k.Value.B }
			end
			return out
		end,
		decode = function(v)
			local keypoints = {}
			for i, k in v do
				keypoints[i] = ColorSequenceKeypoint.new(k[1], Color3.new(k[2], k[3], k[4]))
			end
			return ColorSequence.new(keypoints)
		end,
	},
	Font = {
		encode = function(x)
			return { x.Family, x.Weight.Name, x.Style.Name }
		end,
		decode = function(v)
			return Font.new(v[1], (Enum.FontWeight :: any)[v[2]], (Enum.FontStyle :: any)[v[3]])
		end,
	},
	PhysicalProperties = {
		encode = function(x)
			return { x.Density, x.Friction, x.Elasticity, x.FrictionWeight, x.ElasticityWeight }
		end,
		decode = function(v)
			return PhysicalProperties.new(v[1], v[2], v[3], v[4], v[5])
		end,
	},
}

-- JSON has no NaN or infinity, and one bad value would fail the whole message.
local function isFinite(v: any): boolean
	if type(v) == "number" then
		return v == v and v ~= math.huge and v ~= -math.huge
	elseif type(v) == "table" then
		for _, item in v do
			if not isFinite(item) then
				return false
			end
		end
	end
	return true
end

--[[
	Returns the wire form of value, or nil if it can't be represented: an
	unsupported type, a string JSON can't hold, or an instance refOf doesn't know.
]]
function Values.encode(value: any, refOf: (Instance) -> string?): Wire?
	local kind = typeof(value)

	if kind == "nil" then
		return { t = "Nil" }
	elseif kind == "string" then
		if utf8.len(value) == nil then
			return nil
		end
		return { t = "String", v = value }
	elseif kind == "boolean" then
		return { t = "Bool", v = value }
	elseif kind == "number" then
		if not isFinite(value) then
			return nil
		end
		return { t = "Number", v = value }
	elseif kind == "Instance" then
		local ref = refOf(value)
		if ref == nil then
			return nil
		end
		return { t = "Ref", v = ref }
	elseif kind == "EnumItem" then
		-- Studio prints "Material" and other runtimes "Enum.Material".
		local enumName = string.gsub(tostring(value.EnumType), "^Enum%.", "")
		return { t = "Enum", v = { enumName, value.Name } }
	elseif kind == "Content" then
		if value.SourceType == Enum.ContentSourceType.None then
			return { t = "Content" }
		elseif value.SourceType == Enum.ContentSourceType.Uri then
			return { t = "Content", v = value.Uri }
		end
		return nil -- points at an in-memory object, which can't be saved
	end

	local codec = codecs[kind]
	if codec == nil then
		return nil
	end
	local encoded = codec.encode(value)
	if not isFinite(encoded) then
		return nil
	end
	return { t = kind, v = encoded }
end

--[[
	Returns (true, value) or (false). A false result means the wire value is
	malformed, of an unknown type, or a Ref that resolve doesn't know.
]]
function Values.decode(wire: Wire, resolve: (string) -> Instance?): (boolean, any)
	local t, v = wire.t, wire.v

	if t == "Nil" then
		return true, nil
	elseif t == "String" or t == "Bool" or t == "Number" then
		return true, v
	elseif t == "Ref" then
		local target = resolve(v)
		return target ~= nil, target
	end

	return pcall(function()
		if t == "Enum" then
			local item = (Enum :: any)[v[1]][v[2]]
			assert(item ~= nil, "unknown enum item")
			return item
		elseif t == "Content" then
			if v == nil then
				return Content.none
			end
			return Content.fromUri(v)
		end
		local codec = codecs[t]
		assert(codec ~= nil, "unknown type")
		return codec.decode(v)
	end)
end

-- copies a wire value w/ its numbers rounded, see Values.fingerprint
local function withRoundedNumbers(value: any): any
	if type(value) == "number" then
		return tonumber(string.format("%.12g", value))
	elseif type(value) == "table" then
		local out = {}
		for key, item in value do
			out[key] = withRoundedNumbers(item)
		end
		return out
	end
	return value
end

--[[
	A string that is equal for equal wire values. Sources can be large, so they
	skip JSON encoding.

	numbers are rounded since a value read back from a file can differ from studio's
	in its last bit, depending on the json decoder. most props are 32-bit floats,
	which differ by far more than this when they really changed
]]
function Values.fingerprint(wire: Wire): string
	if wire.t == "String" then
		return "S" .. (wire.v :: any)
	end
	return HttpService:JSONEncode(withRoundedNumbers(wire))
end

return Values
