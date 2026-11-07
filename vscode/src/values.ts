import { Value } from "./protocol";

export interface NumberField {
  label: string;
  value: string;
}

// How the Properties panel shows a value.
export type Editor =
  | { kind: "bool"; value: boolean }
  | { kind: "text"; value: string; hint?: string }
  | { kind: "numbers"; fields: NumberField[] }
  | { kind: "color"; hex: string; text: string }
  | { kind: "readonly"; text: string };

// Number fields for each type that's a flat list of numbers. A CFrame only edits its
// position, and keeps its rotation.
const NUMBER_FIELDS: Record<string, string[]> = {
  Vector2: ["X", "Y"],
  Vector3: ["X", "Y", "Z"],
  CFrame: ["X", "Y", "Z"],
  UDim: ["Scale", "Offset"],
  UDim2: ["X Scale", "X Offset", "Y Scale", "Y Offset"],
  Rect: ["Min X", "Min Y", "Max X", "Max Y"],
  NumberRange: ["Min", "Max"],
};

const SCALAR_NUMBERS = new Set(["Number", "BrickColor"]);

export function describe(value: Value, refName: (ref: string) => string | undefined = () => undefined): Editor {
  const { t, v } = value;
  if (t === "Bool" && typeof v === "boolean") {
    return { kind: "bool", value: v };
  }
  if ((t === "String" && typeof v === "string") || (t === "Content" && (v === undefined || typeof v === "string"))) {
    return { kind: "text", value: (v as string | undefined) ?? "" };
  }
  if (SCALAR_NUMBERS.has(t) && typeof v === "number") {
    return { kind: "numbers", fields: [{ label: "", value: String(v) }] };
  }
  if (t === "Enum" && isStrings(v, 2)) {
    return { kind: "text", value: v[1], hint: v[0] };
  }
  if (t === "Color3" && isNumbers(v, 3)) {
    const bytes = v.map((part) => Math.round(Math.min(Math.max(part, 0), 1) * 255));
    const hex = bytes.map((part) => part.toString(16).padStart(2, "0")).join("");
    return { kind: "color", hex: `#${hex}`, text: bytes.join(", ") };
  }
  const labels = NUMBER_FIELDS[t];
  if (labels && isNumbers(v, t === "CFrame" ? 12 : labels.length)) {
    return { kind: "numbers", fields: labels.map((label, i) => ({ label, value: String(v[i]) })) };
  }
  return { kind: "readonly", text: readonlyText(value, refName) };
}

// The value an edit from the panel makes, or a reason it can't. It's built from the old
// value's type, so nothing the panel sends can change a property's type.
export function applyEdit(old: Value, input: unknown): { value: Value } | { error: string } {
  const editor = describe(old);
  switch (editor.kind) {
    case "bool":
      return typeof input === "boolean" ? { value: { t: old.t, v: input } } : { error: "Expected true or false." };
    case "text":
      if (typeof input !== "string") {
        return { error: "Expected text." };
      }
      if (old.t === "Enum") {
        // Studio looks this up by name, so anything else can only fail there.
        if (!/^[A-Za-z0-9_]+$/.test(input)) {
          return { error: `"${input}" isn't the name of an Enum.${(old.v as string[])[0]} item.` };
        }
        return { value: { t: old.t, v: [(old.v as string[])[0], input] } };
      }
      if (old.t === "Content" && input === "") {
        return { value: { t: old.t } };
      }
      return { value: { t: old.t, v: input } };
    case "numbers": {
      if (!Array.isArray(input) || input.length !== editor.fields.length) {
        return { error: "Expected a number in each field." };
      }
      const numbers = input.map((part) => (typeof part === "string" && part.trim() !== "" ? Number(part) : NaN));
      const bad = numbers.findIndex((part) => !Number.isFinite(part));
      if (bad !== -1) {
        const label = editor.fields[bad].label;
        return { error: `${label ? `${label} needs` : "This needs"} a number.` };
      }
      if (SCALAR_NUMBERS.has(old.t)) {
        return { value: { t: old.t, v: numbers[0] } };
      }
      return { value: { t: old.t, v: [...numbers, ...(old.v as number[]).slice(numbers.length)] } };
    }
    case "color":
      if (typeof input !== "string" || !/^#[0-9a-fA-F]{6}$/.test(input)) {
        return { error: "Expected a color." };
      }
      return {
        value: { t: old.t, v: [1, 3, 5].map((at) => parseInt(input.slice(at, at + 2), 16) / 255) },
      };
    case "readonly":
      return { error: `A ${old.t} can't be edited here.` };
  }
}

function readonlyText(value: Value, refName: (ref: string) => string | undefined): string {
  const { t, v } = value;
  if (t === "Nil" || v === undefined) {
    return "(empty)";
  }
  if (t === "Ref" && typeof v === "string") {
    return refName(v) ?? "(unknown instance)";
  }
  if (t === "Font" && isStrings(v, 3)) {
    return v.join(", ");
  }
  if (t === "NumberSequence" || t === "ColorSequence") {
    return Array.isArray(v) ? `${v.length} keypoints` : JSON.stringify(v);
  }
  if (t === "PhysicalProperties" && isNumbers(v, 5)) {
    return `Density ${v[0]}, Friction ${v[1]}, Elasticity ${v[2]}`;
  }
  return typeof v === "string" ? v : JSON.stringify(v);
}

function isNumbers(v: unknown, length: number): v is number[] {
  return Array.isArray(v) && v.length === length && v.every((part) => typeof part === "number");
}

function isStrings(v: unknown, length: number): v is string[] {
  return Array.isArray(v) && v.length === length && v.every((part) => typeof part === "string");
}
