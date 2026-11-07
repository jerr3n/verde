// `project.yml` is a common name, so this checks it's really Verde's before treating a
// workspace as a Verde project. It's only a first look: the server still validates it.
export function looksLikeVerdeConfig(text: string): boolean {
  return /^serve:/m.test(text) && /^sync:/m.test(text);
}
