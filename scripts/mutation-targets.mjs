// Which mutation packages a change touches.
//
// `make mutations` costs minutes per package, which is why it was the weekly report and not a
// gate. A pull request only has to answer for the packages it changes — those are the ones whose
// tests it could have weakened — so this narrows the sweep to them and stops a survivor from
// waiting until Monday to be noticed.
//
// A package with no line in `.mutationfloors` has no measured floor, so there is nothing to
// compare and it is not offered: gremlins runs a package's own tests, and a package that keeps
// them in the feature suite would report the absence of unit tests rather than the strength of
// them (the note at the bottom of `.mutationfloors` says which, and why).
//
// Weighted by changed lines, because a refactor that touches one file in six packages is six
// sweeps and a rewrite in one is one: `--max` cuts the tail, and the run says which it dropped.
//
// Usage: bun scripts/mutation-targets.mjs [--base origin/main] [--max 2]
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";

const MUTATION = ".mutationfloors";

function git(args) {
	return execFileSync("git", args, { encoding: "utf8" }).trimEnd();
}

function option(name, fallback) {
	const index = process.argv.indexOf(name);
	return index === -1 ? fallback : process.argv[index + 1];
}

const base = option("--base", "origin/main");
const max = Number(option("--max", "2"));
if (!Number.isFinite(max) || max < 1) {
	console.error("mutation-targets: --max needs a positive number");
	process.exit(2);
}

/** The packages `.mutationfloors` names, as `internal/x` and `cmd/x`. */
function flooredPackages() {
	const names = new Set();
	for (const raw of readFileSync(MUTATION, "utf8").split("\n")) {
		const line = raw.trim();
		if (line === "" || line.startsWith("#")) continue;
		const [name] = line.split(/\s+/);
		if (name !== undefined) names.add(name);
	}
	return names;
}

/** A file in the gateway belongs to the first two path segments: `internal/control`, `cmd/acornd`. */
function packageOf(path) {
	const parts = path.split("/");
	if (parts[0] !== "gateway" || parts.length < 3) return undefined;
	return `${parts[1]}/${parts[2]}`;
}

let diff;
try {
	diff = git(["diff", "--numstat", `${base}...HEAD`, "--", "gateway"]);
} catch {
	console.error(`mutation-targets: cannot diff against ${base} — fetch it first (git fetch origin main)`);
	process.exit(1);
}

const floored = flooredPackages();
const weight = new Map();
for (const line of diff.split("\n")) {
	if (line.trim() === "") continue;
	const [added, removed, path] = line.split("\t");
	if (path === undefined) continue;
	const name = packageOf(path);
	if (name === undefined || !floored.has(name)) continue;
	// A binary change reports "-" for both counts: it still changed the package.
	const changed = Number(added) + Number(removed);
	weight.set(name, (weight.get(name) ?? 0) + (Number.isFinite(changed) ? changed : 1));
}

const ranked = [...weight].sort((a, b) => b[1] - a[1]);
const chosen = ranked.slice(0, max);
const dropped = ranked.slice(max);

if (chosen.length === 0) {
	console.error(`mutation-targets: no floored package changed against ${base}, nothing to sweep`);
	process.exit(0);
}

// stdout is the list the workflow hands to `make mutations`; stderr is what a person reads.
for (const [name, lines] of chosen) console.error(`mutation-targets: ${name} (${lines} changed lines)`);
for (const [name, lines] of dropped) {
	console.error(
		`mutation-targets: ${name} changed too (${lines} lines) but is past --max ${max}; ` +
			`run it on demand: make mutations PKG=./${name}`,
	);
}
for (const [name] of chosen) console.log(`./${name}`);
