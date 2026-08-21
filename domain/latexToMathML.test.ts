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
