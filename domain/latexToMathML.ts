/**
 * Pure TypeScript LaTeX to MathML converter for native, zero-dependency
 * mathematical formula rendering in Chromium / Electron.
 */

const GREEK_MAP: Record<string, string> = {
  alpha: "α",
  beta: "β",
  gamma: "γ",
  delta: "δ",
  epsilon: "ε",
  varepsilon: "ε",
  zeta: "ζ",
  eta: "η",
  theta: "θ",
  vartheta: "ϑ",
  iota: "ι",
  kappa: "κ",
  lambda: "λ",
  mu: "μ",
  nu: "ν",
  xi: "ξ",
  pi: "π",
  varpi: "ϖ",
  rho: "ρ",
  varrho: "ϱ",
  sigma: "σ",
  varsigma: "ς",
  tau: "τ",
  upsilon: "υ",
  phi: "φ",
  varphi: "ϕ",
  chi: "χ",
  psi: "ψ",
  omega: "ω",
  Gamma: "Γ",
  Delta: "Δ",
  Theta: "Θ",
  Lambda: "Λ",
  Xi: "Ξ",
  Pi: "Π",
  Sigma: "Σ",
  Upsilon: "Υ",
  Phi: "Φ",
  Psi: "Ψ",
  Omega: "Ω",
};

const SYMBOL_MAP: Record<string, string> = {
  pm: "±",
  mp: "∓",
  times: "×",
  div: "÷",
  cdot: "·",
  ast: "∗",
  star: "⋆",
  circ: "∘",
  bullet: "•",
  cap: "∩",
  cup: "∪",
  uplus: "⊎",
  sqcap: "⊓",
  sqcup: "⊔",
  vee: "∨",
  wedge: "∧",
  setminus: "∖",
  wr: "≀",
  diamond: "⋄",
  leq: "≤",
  le: "≤",
  geq: "≥",
  ge: "≥",
  neq: "≠",
  ne: "≠",
  approx: "≈",
  sim: "∼",
  simeq: "≃",
  cong: "≅",
  equiv: "≡",
  propto: "∝",
  subset: "⊂",
  supset: "⊃",
  subseteq: "⊆",
  supseteq: "⊇",
  in: "∈",
  notin: "∉",
  ni: "∋",
  perp: "⊥",
  mid: "∣",
  parallel: "∥",
  forall: "∀",
  exists: "∃",
  nexists: "∄",
  infty: "∞",
  partial: "∂",
  nabla: "∇",
  emptyset: "∅",
  varnothing: "∅",
  to: "→",
  rightarrow: "→",
  leftarrow: "←",
  Rightarrow: "⇒",
  Leftarrow: "⇐",
  Leftrightarrow: "⇔",
  mapsto: "↦",
  uparrow: "↑",
  downarrow: "↓",
  Uparrow: "⇑",
  Downarrow: "⇓",
  cdots: "⋯",
  ldots: "…",
  vdots: "⋮",
  ddots: "⋱",
  quad: " ",
  prime: "′",
  langle: "⟨",
  rangle: "⟩",
  lbrace: "{",
  rbrace: "}",
  lceil: "⌈",
  rceil: "⌉",
  lfloor: "⌊",
  rfloor: "⌋",
  hbar: "ℏ",
  ell: "ℓ",
  aleph: "ℵ",
  top: "⊤",
  bot: "⊥",
  iff: "⟺",
  implies: "⟹",
  dots: "…",
};

const OPERATOR_MAP: Record<string, string> = {
  sum: "∑",
  prod: "∏",
  coprod: "∐",
  int: "∫",
  iint: "∬",
  iiint: "∭",
  oint: "∮",
  lim: "lim",
  max: "max",
  min: "min",
  sup: "sup",
  inf: "inf",
};

const FUNCTION_NAMES = new Set([
  "sin", "cos", "tan", "cot", "sec", "csc",
  "arcsin", "arccos", "arctan",
  "sinh", "cosh", "tanh", "coth",
  "ln", "log", "exp", "det", "dim", "ker",
  "deg", "gcd", "hom", "Pr",
]);

export interface LatexToken {
  type: "command" | "char" | "number" | "symbol" | "group" | "sub" | "sup" | "space" | "linebreak" | "amp";
  value: string;
  children?: LatexToken[];
}

export function tokenizeLatex(input: string): LatexToken[] {
  const tokens: LatexToken[] = [];
  let i = 0;
  const len = input.length;

  while (i < len) {
    const ch = input[i];

    if (ch === " " || ch === "\t" || ch === "\n" || ch === "\r") {
      i++;
      continue;
    }

    if (ch === "\\") {
      i++;
      if (i >= len) break;
      const nextChar = input[i];

      // Handle special escapes
      if (nextChar === "\\" || nextChar === "\n") {
        tokens.push({ type: "linebreak", value: "\\\\" });
        i++;
        continue;
      }
      if (nextChar === "{" || nextChar === "}" || nextChar === "$" || nextChar === "%" || nextChar === "&" || nextChar === "#" || nextChar === "_") {
        tokens.push({ type: "char", value: nextChar });
        i++;
        continue;
      }
      if (nextChar === "," || nextChar === ";" || nextChar === " ") {
        tokens.push({ type: "space", value: " " });
        i++;
        continue;
      }
      if (nextChar === "!") {
        // negative space, ignore
        i++;
        continue;
      }

      // Read command name [a-zA-Z]+
      let name = "";
      while (i < len && /[a-zA-Z]/.test(input[i])) {
        name += input[i];
        i++;
      }
      tokens.push({ type: "command", value: name });
      continue;
    }

    if (ch === "{") {
      i++;
      let depth = 1;
      let groupContent = "";
      while (i < len && depth > 0) {
        if (input[i] === "{" && (i === 0 || input[i - 1] !== "\\")) depth++;
        else if (input[i] === "}" && (i === 0 || input[i - 1] !== "\\")) {
          depth--;
          if (depth === 0) {
            i++;
            break;
          }
        }
        groupContent += input[i];
        i++;
      }
      tokens.push({
        type: "group",
        value: groupContent,
        children: tokenizeLatex(groupContent),
      });
      continue;
    }

    if (ch === "_") {
      tokens.push({ type: "sub", value: "_" });
      i++;
      continue;
    }

    if (ch === "^") {
      tokens.push({ type: "sup", value: "^" });
      i++;
      continue;
    }

    if (ch === "&") {
      tokens.push({ type: "amp", value: "&" });
      i++;
      continue;
    }

    if (/[0-9]/.test(ch)) {
      let num = "";
      while (i < len && /[0-9.]/.test(input[i])) {
        num += input[i];
        i++;
      }
      tokens.push({ type: "number", value: num });
      continue;
    }

    if (/[a-zA-Z]/.test(ch)) {
      tokens.push({ type: "char", value: ch });
      i++;
      continue;
    }

    // Punctuation and symbols
    tokens.push({ type: "symbol", value: ch });
    i++;
  }

  return tokens;
}

function escapeXml(unsafe: string): string {
  return unsafe
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&apos;");
}

function getNextArg(tokens: LatexToken[], index: number): { arg: LatexToken | null; nextIndex: number } {
  if (index >= tokens.length) return { arg: null, nextIndex: index };
  const tok = tokens[index];
  return { arg: tok, nextIndex: index + 1 };
}

function getNextOptionalBracketArg(tokens: LatexToken[], index: number): {
  hasOpt: boolean;
  optTokens: LatexToken[];
  nextIndex: number;
} {
  if (index >= tokens.length) return { hasOpt: false, optTokens: [], nextIndex: index };
  const first = tokens[index];
  if (first.value === "[") {
    const optTokens: LatexToken[] = [];
    let j = index + 1;
    let depth = 1;
    while (j < tokens.length && depth > 0) {
      const t = tokens[j];
      if (t.value === "[") depth++;
      else if (t.value === "]") {
        depth--;
        if (depth === 0) {
          j++;
          break;
        }
      }
      optTokens.push(t);
      j++;
    }
    return { hasOpt: true, optTokens, nextIndex: j };
  }
  return { hasOpt: false, optTokens: [], nextIndex: index };
}

function renderTokensToMathML(tokens: LatexToken[]): string {
  let result = "";
  let i = 0;

  while (i < tokens.length) {
    const tok = tokens[i];

    // Check for base with attached sub/sup
    let baseXml = "";
    let consumed = false;

    if (tok.type === "command") {
      const cmd = tok.value;

      if (cmd === "frac" || cmd === "dfrac" || cmd === "tfrac") {
        const numRes = getNextArg(tokens, i + 1);
        const denRes = getNextArg(tokens, numRes.nextIndex);
        i = denRes.nextIndex;

        const numXml = numRes.arg ? renderTokensToMathML(numRes.arg.children || [numRes.arg]) : "";
        const denXml = denRes.arg ? renderTokensToMathML(denRes.arg.children || [denRes.arg]) : "";
        baseXml = `<mfrac><mrow>${numXml}</mrow><mrow>${denXml}</mrow></mfrac>`;
        consumed = true;
      } else if (cmd === "binom" || cmd === "tbinom" || cmd === "dbinom") {
        const topRes = getNextArg(tokens, i + 1);
        const botRes = getNextArg(tokens, topRes.nextIndex);
        i = botRes.nextIndex;

        const topXml = topRes.arg ? renderTokensToMathML(topRes.arg.children || [topRes.arg]) : "";
        const botXml = botRes.arg ? renderTokensToMathML(botRes.arg.children || [botRes.arg]) : "";
        baseXml = `<mrow><mo fence="true">(</mo><mfrac linethickness="0"><mrow>${topXml}</mrow><mrow>${botXml}</mrow></mfrac><mo fence="true">)</mo></mrow>`;
        consumed = true;
      } else if (cmd === "sqrt") {
        const optRes = getNextOptionalBracketArg(tokens, i + 1);
        const argRes = getNextArg(tokens, optRes.nextIndex);
        i = argRes.nextIndex;

        const argXml = argRes.arg ? renderTokensToMathML(argRes.arg.children || [argRes.arg]) : "";
        if (optRes.hasOpt) {
          const indexXml = renderTokensToMathML(optRes.optTokens);
          baseXml = `<mroot><mrow>${argXml}</mrow><mrow>${indexXml}</mrow></mroot>`;
        } else {
          baseXml = `<msqrt><mrow>${argXml}</mrow></msqrt>`;
        }
        consumed = true;
      } else if (cmd === "overline") {
        const argRes = getNextArg(tokens, i + 1);
        i = argRes.nextIndex;
        const argXml = argRes.arg ? renderTokensToMathML(argRes.arg.children || [argRes.arg]) : "";
        baseXml = `<mover><mrow>${argXml}</mrow><mo stretchy="true">¯</mo></mover>`;
        consumed = true;
      } else if (cmd === "underline") {
        const argRes = getNextArg(tokens, i + 1);
        i = argRes.nextIndex;
        const argXml = argRes.arg ? renderTokensToMathML(argRes.arg.children || [argRes.arg]) : "";
        baseXml = `<munder><mrow>${argXml}</mrow><mo stretchy="true">_</mo></munder>`;
        consumed = true;
      } else if (
        cmd === "text" ||
        cmd === "mathrm" ||
        cmd === "mathbf" ||
        cmd === "mathit" ||
        cmd === "mathbb" ||
        cmd === "mathcal" ||
        cmd === "bm" ||
        cmd === "boldsymbol" ||
        cmd === "operatorname"
      ) {
        const argRes = getNextArg(tokens, i + 1);
        i = argRes.nextIndex;
        const rawText = argRes.arg
          ? argRes.arg.children
            ? renderTokensToMathML(argRes.arg.children)
            : argRes.arg.value
          : "";
        if (cmd === "mathbb") {
          const bboardMap: Record<string, string> = {
            R: "ℝ", N: "ℕ", Z: "ℤ", Q: "ℚ", C: "ℂ", P: "ℙ", H: "ℍ", E: "𝔼",
          };
          const bboard = bboardMap[rawText.trim()] || rawText;
          baseXml = `<mi>${escapeXml(bboard)}</mi>`;
        } else if (cmd === "mathrm" || cmd === "operatorname") {
          baseXml = `<mi mathvariant="normal">${rawText.includes("<") ? rawText : escapeXml(rawText)}</mi>`;
        } else if (cmd === "mathbf" || cmd === "bm" || cmd === "boldsymbol") {
          baseXml = `<mi mathvariant="bold">${rawText.includes("<") ? rawText : escapeXml(rawText)}</mi>`;
        } else {
          baseXml = `<mtext>${escapeXml(rawText)}</mtext>`;
        }
        consumed = true;
      } else if (cmd === "left") {
        const delimRes = getNextArg(tokens, i + 1);
        i = delimRes.nextIndex;
        const delim = delimRes.arg ? delimRes.arg.value : "(";
        baseXml = `<mo fence="true" stretchy="true">${escapeXml(delim === "." ? "" : delim)}</mo>`;
        consumed = true;
      } else if (cmd === "right") {
        const delimRes = getNextArg(tokens, i + 1);
        i = delimRes.nextIndex;
        const delim = delimRes.arg ? delimRes.arg.value : ")";
        baseXml = `<mo fence="true" stretchy="true">${escapeXml(delim === "." ? "" : delim)}</mo>`;
        consumed = true;
      } else if (cmd === "begin") {
        const envRes = getNextArg(tokens, i + 1);
        const envName = envRes.arg ? envRes.arg.value.trim() : "matrix";
        i = envRes.nextIndex;

        // Collect tokens until \end{envName}
        const matrixTokens: LatexToken[] = [];
        while (i < tokens.length) {
          const t = tokens[i];
          if (t.type === "command" && t.value === "end") {
            i++; // skip end
            i++; // skip env arg
            break;
          }
          matrixTokens.push(t);
          i++;
        }

        // Parse matrix rows by linebreak and columns by amp
        const rows: LatexToken[][][] = [[]];
        let currentCell: LatexToken[] = [];

        for (const mt of matrixTokens) {
          if (mt.type === "linebreak") {
            rows[rows.length - 1].push(currentCell);
            currentCell = [];
            rows.push([]);
          } else if (mt.type === "amp") {
            rows[rows.length - 1].push(currentCell);
            currentCell = [];
          } else {
            currentCell.push(mt);
          }
        }
        if (currentCell.length > 0 || rows[rows.length - 1].length > 0) {
          rows[rows.length - 1].push(currentCell);
        }

        let matrixXml = "<mtable>";
        for (const row of rows) {
          if (row.length === 0) continue;
          matrixXml += "<mtr>";
          for (const cell of row) {
            matrixXml += `<mtd><mrow>${renderTokensToMathML(cell)}</mrow></mtd>`;
          }
          matrixXml += "</mtr>";
        }
        matrixXml += "</mtable>";

        if (envName === "pmatrix") {
          baseXml = `<mo fence="true">(</mo>${matrixXml}<mo fence="true">)</mo>`;
        } else if (envName === "bmatrix") {
          baseXml = `<mo fence="true">[</mo>${matrixXml}<mo fence="true">]</mo>`;
        } else if (envName === "Bmatrix") {
          baseXml = `<mo fence="true">{</mo>${matrixXml}<mo fence="true">}</mo>`;
        } else if (envName === "vmatrix") {
          baseXml = `<mo fence="true">|</mo>${matrixXml}<mo fence="true">|</mo>`;
        } else if (envName === "Vmatrix") {
          baseXml = `<mo fence="true">‖</mo>${matrixXml}<mo fence="true">‖</mo>`;
        } else if (envName === "cases") {
          baseXml = `<mo fence="true">{</mo>${matrixXml}`;
        } else {
          baseXml = matrixXml;
        }
        consumed = true;
      } else if (GREEK_MAP[cmd]) {
        baseXml = `<mi>${GREEK_MAP[cmd]}</mi>`;
        i++;
        consumed = true;
      } else if (SYMBOL_MAP[cmd]) {
        baseXml = `<mo>${SYMBOL_MAP[cmd]}</mo>`;
        i++;
        consumed = true;
      } else if (OPERATOR_MAP[cmd]) {
        baseXml = `<mo class="math-operator">${OPERATOR_MAP[cmd]}</mo>`;
        i++;
        consumed = true;
      } else if (FUNCTION_NAMES.has(cmd)) {
        baseXml = `<mo>${cmd}</mo>`;
        i++;
        consumed = true;
      } else if (cmd === "hat" || cmd === "bar" || cmd === "vec" || cmd === "dot" || cmd === "ddot" || cmd === "tilde") {
        const argRes = getNextArg(tokens, i + 1);
        i = argRes.nextIndex;
        const argXml = argRes.arg ? renderTokensToMathML(argRes.arg.children || [argRes.arg]) : "";
        const accentSymbols: Record<string, string> = {
          hat: "^", bar: "¯", vec: "→", dot: "˙", ddot: "¨", tilde: "~",
        };
        baseXml = `<mover><mrow>${argXml}</mrow><mo>${accentSymbols[cmd] || "^"}</mo></mover>`;
        consumed = true;
      } else {
        baseXml = `<mi>${escapeXml(cmd)}</mi>`;
        i++;
        consumed = true;
      }
    } else if (tok.type === "number") {
      baseXml = `<mn>${escapeXml(tok.value)}</mn>`;
      i++;
      consumed = true;
    } else if (tok.type === "char") {
      baseXml = `<mi>${escapeXml(tok.value)}</mi>`;
      i++;
      consumed = true;
    } else if (tok.type === "symbol") {
      baseXml = `<mo>${escapeXml(tok.value)}</mo>`;
      i++;
      consumed = true;
    } else if (tok.type === "group") {
      baseXml = `<mrow>${renderTokensToMathML(tok.children || [])}</mrow>`;
      i++;
      consumed = true;
    } else if (tok.type === "space") {
      baseXml = `<mspace width="0.25em" />`;
      i++;
      consumed = true;
    } else if (tok.type === "linebreak") {
      baseXml = `<mspace linebreak="newline" />`;
      i++;
      consumed = true;
    } else {
      i++;
    }

    if (!consumed) continue;

    // Check if followed by subscript _ or superscript ^
    let subXml: string | null = null;
    let supXml: string | null = null;

    while (i < tokens.length) {
      if (tokens[i].type === "sub") {
        const next = getNextArg(tokens, i + 1);
        i = next.nextIndex;
        subXml = next.arg ? renderTokensToMathML(next.arg.children || [next.arg]) : "";
      } else if (tokens[i].type === "sup") {
        const next = getNextArg(tokens, i + 1);
        i = next.nextIndex;
        supXml = next.arg ? renderTokensToMathML(next.arg.children || [next.arg]) : "";
      } else {
        break;
      }
    }

    if (subXml && supXml) {
      // If base is a large operator like \sum or \int, use munderover
      if (baseXml.includes("math-operator")) {
        result += `<munderover>${baseXml}<mrow>${subXml}</mrow><mrow>${supXml}</mrow></munderover>`;
      } else {
        result += `<msubsup>${baseXml}<mrow>${subXml}</mrow><mrow>${supXml}</mrow></msubsup>`;
      }
    } else if (subXml) {
      if (baseXml.includes("math-operator")) {
        result += `<munder>${baseXml}<mrow>${subXml}</mrow></munder>`;
      } else {
        result += `<msub>${baseXml}<mrow>${subXml}</mrow></msub>`;
      }
    } else if (supXml) {
      if (baseXml.includes("math-operator")) {
        result += `<mover>${baseXml}<mrow>${supXml}</mrow></mover>`;
      } else {
        result += `<msup>${baseXml}<mrow>${supXml}</mrow></msup>`;
      }
    } else {
      result += baseXml;
    }
  }

  return result;
}

/**
 * Converts a LaTeX math expression into standard MathML string.
 */
export function latexToMathML(latex: string, displayMode = true): string {
  const clean = latex.trim().replace(/^\\\[|\\\]$/g, "").replace(/^\$\$|\$\$$/g, "");
  if (!clean) return "";

  try {
    const tokens = tokenizeLatex(clean);
    const body = renderTokensToMathML(tokens);
    const displayAttr = displayMode ? ' display="block"' : ' display="inline"';
    return `<math xmlns="http://www.w3.org/1998/Math/MathML"${displayAttr} class="netcatty-math-equation"><mrow>${body}</mrow></math>`;
  } catch {
    // Fallback: safe raw rendering
    return `<math xmlns="http://www.w3.org/1998/Math/MathML" display="block" class="netcatty-math-equation"><mtext>${escapeXml(clean)}</mtext></math>`;
  }
}
