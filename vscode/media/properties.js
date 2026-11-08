// SPDX-License-Identifier: MPL-2.0

// Draws the panel state from src/panel.ts and reports edits. Everything is set through
// textContent and value, because names and values come from project files.
(function () {
  const vscode = acquireVsCodeApi();
  const root = document.getElementById("root");

  let shownId;
  let waiting; // the latest state, held back while the user is typing

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) {
      node.className = className;
    }
    if (text !== undefined) {
      node.textContent = text;
    }
    return node;
  }

  function textInput(value, onCommit, label) {
    const input = el("input");
    input.type = "text";
    input.value = value;
    input.spellcheck = false;
    if (label) {
      input.setAttribute("aria-label", label);
    }
    input.addEventListener("change", () => onCommit(input.value));
    input.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        input.blur();
      } else if (event.key === "Escape") {
        input.value = value;
        input.blur();
      }
    });
    return input;
  }

  function editorFor(row, id) {
    const edit = (input) => vscode.postMessage({ type: "edit", id, name: row.name, input });
    const { editor } = row;
    const box = el("div", "fields");

    switch (editor.kind) {
      case "bool": {
        const input = el("input");
        input.type = "checkbox";
        input.checked = editor.value;
        input.setAttribute("aria-label", row.name);
        input.addEventListener("change", () => edit(input.checked));
        box.append(input);
        break;
      }
      case "text": {
        const field = el("label", "field");
        field.append(textInput(editor.value, edit, row.name));
        if (editor.hint) {
          field.append(el("span", "hint", editor.hint));
        }
        box.append(field);
        break;
      }
      case "numbers": {
        const inputs = [];
        const commit = () => edit(inputs.map((input) => input.value));
        for (const { label, value } of editor.fields) {
          const field = el("label", "field");
          if (label) {
            field.append(el("span", "", label));
          }
          const input = textInput(value, commit, label ? `${row.name} ${label}` : row.name);
          input.inputMode = "decimal";
          inputs.push(input);
          field.append(input);
          box.append(field);
        }
        break;
      }
      case "color": {
        const wrap = el("div", "color");
        const input = el("input");
        input.type = "color";
        input.value = editor.hex;
        input.setAttribute("aria-label", row.name);
        const text = el("span", "hint", editor.text);
        input.addEventListener("change", () => edit(input.value));
        wrap.append(input, text);
        box.append(wrap);
        break;
      }
      default:
        box.append(el("div", "value", editor.text));
    }
    return box;
  }

  function section(title, rows, id) {
    const out = [el("h2", "", title)];
    for (const row of rows) {
      const line = el("div", "row");
      const label = el("div", "label", row.name);
      label.title = row.type;
      line.append(label, editorFor(row, id));
      out.push(line);
    }
    return out;
  }

  function render(state) {
    const { panel, message } = state;
    waiting = undefined;
    shownId = panel ? panel.id : undefined;

    if (!panel) {
      root.replaceChildren(el("p", "empty", message));
      return;
    }

    const nodes = [];
    const header = el("div", "header");
    const title = el("div", "title");
    title.title = panel.path;
    title.append(el("div", "name", panel.name), el("div", "path", panel.class));
    header.append(title);
    if (panel.canOpen) {
      const open = el("button", "", "Open");
      open.title = "Open this instance's file";
      open.addEventListener("click", () => vscode.postMessage({ type: "open", id: panel.id }));
      header.append(open);
    }
    nodes.push(header);

    if (panel.properties.length > 0) {
      nodes.push(...section("Properties", panel.properties, panel.id));
    }
    if (panel.attributes.length > 0) {
      nodes.push(...section("Attributes", panel.attributes, panel.id));
    }
    if (panel.tags.length > 0) {
      nodes.push(el("h2", "", "Tags"));
      const tags = el("div", "tags");
      tags.append(...panel.tags.map((tag) => el("span", "tag", tag)));
      nodes.push(tags);
    }
    nodes.push(
      el(
        "p",
        "note",
        panel.isMount
          ? "A service's own properties aren't synced."
          : "Only values that differ from a new instance are stored, so the rest don't appear here.",
      ),
    );
    root.replaceChildren(...nodes);
  }

  function isTyping() {
    const active = document.activeElement;
    return active instanceof HTMLInputElement && active.type === "text";
  }

  window.addEventListener("message", (event) => {
    const state = event.data;
    if (state.type !== "state") {
      return;
    }
    // Redrawing would throw away what's being typed. Selecting something else still wins.
    const sameInstance = state.panel && state.panel.id === shownId;
    if (!state.force && sameInstance && isTyping()) {
      waiting = state;
      return;
    }
    render(state);
  });

  // Tabbing between fields moves focus straight to the next one, so this looks after it settles.
  document.addEventListener("focusout", () => {
    setTimeout(() => {
      if (waiting && !isTyping()) {
        render(waiting);
      }
    }, 0);
  });

  vscode.postMessage({ type: "ready" });
})();
