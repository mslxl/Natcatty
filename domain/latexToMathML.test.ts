import test from "node:test";
import assert from "node:assert/strict";
import { latexToMathML, tokenizeLatex } from "./latexToMathML.ts";

test("tokenizeLatex tokenizes basic commands and superscripts", () => {
  const tokens = tokenizeLatex("E = mc^2");
  assert.equal(tokens[0].type, "char");
  assert.equal(tokens[0].value, "E");
  assert.equal(tokens[1].type, "symbol");
  assert.equal(tokens[1].value, "=");
  assert.equal(tokens[2].type, "char");
  assert.equal(tokens[2].value, "m");
  assert.equal(tokens[3].type, "char");
  assert.equal(tokens[3].value, "c");
  assert.equal(tokens[4].type, "sup");
  assert.equal(tokens[5].type, "number");
  assert.equal(tokens[5].value, "2");
});

test("latexToMathML converts E = mc^2 correctly", () => {
  const mathml = latexToMathML("E = mc^2");
  assert.ok(mathml.includes("<math"));
  assert.ok(mathml.includes("<msup>"));
  assert.ok(mathml.includes("<mn>2</mn>"));
});

test("latexToMathML converts fractions and square roots", () => {
  const mathml = latexToMathML("\\frac{a}{b} + \\sqrt{x}");
  assert.ok(mathml.includes("<mfrac>"));
  assert.ok(mathml.includes("<msqrt>"));
});

test("latexToMathML converts Greek letters and operators", () => {
  const mathml = latexToMathML("\\sum_{i=1}^n \\alpha_i \\times \\beta_i");
  assert.ok(mathml.includes("∑"));
  assert.ok(mathml.includes("α"));
  assert.ok(mathml.includes("β"));
  assert.ok(mathml.includes("×"));
});

test("latexToMathML converts matrices", () => {
  const mathml = latexToMathML("\\begin{pmatrix} a & b \\\\ c & d \\end{pmatrix}");
  assert.ok(mathml.includes("<mtable>"));
  assert.ok(mathml.includes("<mtr>"));
  assert.ok(mathml.includes("<mtd>"));
});

test("latexToMathML converts n-th root \\sqrt[3]{3} correctly using mroot", () => {
  const mathml = latexToMathML("\\sqrt[3]{3}");
  assert.ok(mathml.includes("<mroot>"));
  assert.ok(mathml.includes("<mn>3</mn>"));
});

test("latexToMathML converts binomial and overline", () => {
  const mathml = latexToMathML("\\binom{n}{k} + \\overline{z}");
  assert.ok(mathml.includes("linethickness=\"0\""));
  assert.ok(mathml.includes("<mover>"));
});

test("latexToMathML converts \\textstyle, \\displaystyle, and script styles", () => {
  const textstyleMath = latexToMathML("\\textstyle \\sum_{i=1}^n x_i");
  assert.ok(textstyleMath.includes("<mstyle displaystyle=\"false\" scriptlevel=\"0\">"));
  assert.ok(textstyleMath.includes("∑"));

  const groupedTextstyle = latexToMathML("{\\textstyle \\frac{1}{2}} + \\frac{3}{4}");
  assert.ok(groupedTextstyle.includes("<mstyle displaystyle=\"false\" scriptlevel=\"0\"><mfrac>"));

  const displaystyleMath = latexToMathML("\\displaystyle \\int_0^1 f(x) dx");
  assert.ok(displaystyleMath.includes("<mstyle displaystyle=\"true\" scriptlevel=\"0\">"));

  const scriptstyleMath = latexToMathML("\\scriptstyle a + b");
  assert.ok(scriptstyleMath.includes("<mstyle displaystyle=\"false\" scriptlevel=\"1\">"));
});

test("latexToMathML converts text formatting commands", () => {
  const mathml = latexToMathML("\\text{hello world} + \\textbf{bold} + \\textit{italic}");
  assert.ok(mathml.includes("<mtext>hello world</mtext>"));
  assert.ok(mathml.includes("<mtext mathvariant=\"bold\">bold</mtext>"));
  assert.ok(mathml.includes("<mtext mathvariant=\"italic\">italic</mtext>"));
});

test("latexToMathML preserves math font arguments without escaping rendered tokens", () => {
  const mathml = latexToMathML("\\mathbb{R} + \\mathbf{x} + \\mathit{y} + \\mathcal{F}");
  assert.ok(mathml.includes("<mi>ℝ</mi>"));
  assert.ok(mathml.includes('<mstyle mathvariant="bold"><mrow><mi>x</mi></mrow></mstyle>'));
  assert.ok(mathml.includes('<mstyle mathvariant="italic"><mrow><mi>y</mi></mrow></mstyle>'));
  assert.ok(mathml.includes('<mstyle mathvariant="script"><mrow><mi>F</mi></mrow></mstyle>'));
  assert.ok(!mathml.includes("&lt;mi&gt;"));
});

test("latexToMathML resolves command delimiters after left and right", () => {
  const mathml = latexToMathML("\\left\\langle x \\right\\rangle");
  assert.ok(mathml.includes('<mo fence="true" stretchy="true">⟨</mo>'));
  assert.ok(mathml.includes('<mo fence="true" stretchy="true">⟩</mo>'));
  assert.ok(!mathml.includes(">langle<"));
  assert.ok(!mathml.includes(">rangle<"));

  const doubleBar = latexToMathML("\\left\\lVert x \\right\\rVert");
  assert.equal(doubleBar.match(/>∥<\/mo>/g)?.length, 2);

  const escapedBar = latexToMathML("\\left\\| x \\right\\|");
  assert.equal(escapedBar.match(/>∥<\/mo>/g)?.length, 2);

  const inlineEscapedBar = latexToMathML("\\|x\\|");
  assert.equal(inlineEscapedBar.match(/<mo>∥<\/mo>/g)?.length, 2);

  const backslash = latexToMathML("\\left\\backslash x \\right\\backslash");
  assert.equal(backslash.match(/>\\<\/mo>/g)?.length, 2);
});
