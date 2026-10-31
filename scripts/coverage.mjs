// Coverage across both processes, joined and judged.
//
// `go test -cover` cannot see the gateway. The feature suite starts it as its own process, so
// for years the end-to-end half of the suite was worth exactly nothing to the number — which is
// why "we do not know what is covered" was true and not a complaint about habits. The gateway
// binary is now built for coverage and writes its counters to GOCOVERDIR; this joins those with
// the unit profile so a package's figure is the whole package, and applies the floor.
//
// Usage:
//   bun scripts/coverage.mjs merge <out.cover> <in.cover>...
//   bun scripts/coverage.mjs report <in.cover> [--floor 95] [--ignore .coverageignore]
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const MODULE = "acornd/gateway/";

// The profiles spell a file as its import path; the ignore list and the table are easier to
// read, and to review, relative to the gateway.
function local(path) {
	const at = path.indexOf(MODULE);
	return at === -1 ? path : path.slice(at + MODULE.length);
}

// Go writes `path:start,end statements count`, one block per line. The block is the key and
// the count is what adds: a block covered by both halves is still that many statements.
function merge(out, inputs) {
	const merged = new Map();
	for (const input of inputs) {
		for (const line of readFileSync(input, "utf8").split("\n")) {
			if (line === "" || line.startsWith("mode:")) continue;
			const key = line.slice(0, line.lastIndexOf(" "));
			const count = Number(line.slice(line.lastIndexOf(" ") + 1));
			if (!Number.isFinite(count)) throw new Error(`${input}: cannot read "${line}"`);
			merged.set(key, (merged.get(key) ?? 0) + count);
		}
	}
	const lines = [...merged].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)).map(([key, count]) => `${key} ${count}`);
	writeFileSync(out, `mode: atomic\n${lines.join("\n")}\n`);
	console.log(`coverage: merged ${inputs.length} profiles, ${lines.length} blocks -> ${out}`);
}

// An exclusion without a reason is the rule losing quietly, which is what S9 forbids — so a
// bare path fails here rather than being honoured.
function exclusions(path) {
	const entries = [];
	for (const line of readFileSync(path, "utf8").split("\n")) {
		const text = line.trim();
		if (text === "" || text.startsWith("#")) continue;
		const [pattern, reason] = text.split(" -- ");
		if (!pattern || !reason || reason.trim() === "") {
			throw new Error(`${path}: "${text}" has no reason; write "<path> -- <why it cannot be covered>"`);
		}
		entries.push({ pattern: pattern.trim(), reason: reason.trim() });
	}
	return entries;
}

function covers(pattern, file) {
	return file === pattern || file.startsWith(pattern.endsWith("/") ? pattern : `${pattern}/`);
}

// A floor per package, because one number for the whole repo says nothing about a package that
// carries its weight and one that does not. The file is where a floor may move, and it may only
// move up: a ratchet is the difference between a bar and a wish.
function floors(path, fallback) {
	const perPackage = new Map();
	const reasons = new Map();
	for (const line of readFileSync(path, "utf8").split("\n")) {
		const text = line.trim();
		if (text === "" || text.startsWith("#")) continue;
		// Split at the first gap rather than on whitespace: a `split(/\s+/, 2)` keeps two tokens
		// and throws the rest of the line away, which is where a package's reason lives.
		const gap = text.search(/\s/);
		const name = gap === -1 ? text : text.slice(0, gap);
		const rest = gap === -1 ? "" : text.slice(gap).trim();
		const value = Number(rest.split(" ")[0]);
		if (name === undefined || !Number.isFinite(value)) throw new Error(`${path}: cannot read "${text}"`);
		perPackage.set(name, value);
		// The clause after the number is the reason this package cannot reach the target, and a
		// reason is not the same thing as work left to do. The report says which is which.
		const clause = rest?.includes("--") ? rest.slice(rest.indexOf("--") + 2).trim() : "";
		if (clause !== "") reasons.set(name, clause);
	}
	const floor = (name) => perPackage.get(name) ?? fallback;
	return Object.assign(floor, { reason: (name) => reasons.get(name) ?? "" });
}

function report(profile, floor, ignorePath, root, floorPath) {
	const floorFor = floors(floorPath, floor);
	const ignored = exclusions(ignorePath);
	const packages = new Map();
	const excluded = { statements: 0, covered: 0, entries: new Map() };

	for (const line of readFileSync(profile, "utf8").split("\n")) {
		if (line === "" || line.startsWith("mode:")) continue;
		const [key, statements, count] = line.split(" ");
		const file = local(key.slice(0, key.indexOf(":")));
		const statementsHere = Number(statements);
		const coveredHere = Number(count) > 0 ? statementsHere : 0;

		const entry = ignored.find((candidate) => covers(candidate.pattern, file));
		if (entry !== undefined) {
			excluded.statements += statementsHere;
			excluded.covered += coveredHere;
			excluded.entries.set(entry.pattern, (excluded.entries.get(entry.pattern) ?? 0) + statementsHere);
			continue;
		}
		const name = file.includes("/") ? file.slice(0, file.lastIndexOf("/")) : ".";
		const pkg = packages.get(name) ?? { statements: 0, covered: 0 };
		pkg.statements += statementsHere;
		pkg.covered += coveredHere;
		packages.set(name, pkg);
	}

	const percent = (covered, statements) => (statements === 0 ? 100 : (covered / statements) * 100);
	const rows = [...packages].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));

	const width = Math.max(...rows.map(([name]) => name.length), "package".length);
	console.log(`\n${"package".padEnd(width)}  statements   covered      %  floor`);
	let totalStatements = 0;
	let totalCovered = 0;
	const below = [];
	for (const [name, pkg] of rows) {
		totalStatements += pkg.statements;
		totalCovered += pkg.covered;
		const value = percent(pkg.covered, pkg.statements);
		const own = floorFor(name);
		if (value < own) below.push(`${name} ${value.toFixed(1)}% (floor ${own}%)`);
		console.log(
			`${name.padEnd(width)}  ${String(pkg.statements).padStart(10)}  ${String(pkg.covered).padStart(9)}  ${value.toFixed(1).padStart(5)}  ${String(floorFor(name)).padStart(5)}${value < floorFor(name) ? " below" : ""}`,
		);
	}
	const all = totalStatements + excluded.statements;
	console.log(`${"total".padEnd(width)}  ${String(totalStatements).padStart(10)}  ${String(totalCovered).padStart(9)}  ${percent(totalCovered, totalStatements).toFixed(1).padStart(5)}      -`);

	// The cap is the point of the exclude file: a floor that the excluded part can grow around
	// is a floor in name only.
	const excludedShare = all === 0 ? 0 : (excluded.statements / all) * 100;
	console.log(`\nexcluded: ${excluded.statements} statements, ${excludedShare.toFixed(1)}% of ${all} (cap 3.0%)`);
	for (const [pattern, statements] of excluded.entries) console.log(`  ${String(statements).padStart(6)}  ${pattern}`);

	const problems = [];
	if (excludedShare > 3) problems.push(`exclusions are ${excludedShare.toFixed(1)}% of all statements, over the 3% cap`);
	if (below.length > 0) problems.push(`${below.length} package(s) below their floor: ${below.join(", ")}`);
	for (const entry of ignored) {
		if (excluded.entries.has(entry.pattern)) continue;
		// A platform file is not compiled on this host, so it appears in no profile and there is
		// nothing to exclude. A file that is not in the tree at all is a rename someone forgot:
		// that is the one worth failing on.
		if (!existsSync(join(root, "gateway", entry.pattern))) {
			problems.push(`${entry.pattern} is ignored but is not in the tree: drop the line or fix the path`);
		}
	}
	if (problems.length > 0) {
		console.error(`\ncoverage: not met\n  - ${problems.join("\n  - ")}`);
		process.exit(1);
	}
	// Below the target is two different things: work left to do, and a package whose floor line
	// carries the reason it cannot get there. Printing them together made the second look like the
	// first, which is how an exception stops being exceptional.
	const short = rows.filter(([name, pkg]) => percent(pkg.covered, pkg.statements) < 95).map(([name]) => name);
	const grinding = short.filter((name) => floorFor.reason(name) === "");
	const excepted = short.filter((name) => floorFor.reason(name) !== "");
	console.log(`\ncoverage: every package at or above its floor`);
	if (grinding.length > 0) {
		console.log(`to reach 95%: ${grinding.join(", ")}`);
	}
	for (const name of excepted) {
		console.log(`excepted at ${floorFor(name)}%: ${name} — ${floorFor.reason(name)}`);
	}
}

const [command, out, ...rest] = process.argv.slice(2);
if (command === "merge" && out !== undefined && rest.length > 0) {
	merge(out, rest);
} else if (command === "report" && out !== undefined) {
	const floorFlag = rest.indexOf("--floor");
	const ignoreFlag = rest.indexOf("--ignore");
	const rootFlag = rest.indexOf("--root");
	const floorsFlag = rest.indexOf("--floors");
	const floor = floorFlag === -1 ? 95 : Number(rest[floorFlag + 1]);
	const ignore = ignoreFlag === -1 ? ".coverageignore" : rest[ignoreFlag + 1];
	const root = rootFlag === -1 ? "." : rest[rootFlag + 1];
	const floorFile = floorsFlag === -1 ? ".coveragefloors" : rest[floorsFlag + 1];
	if (!Number.isFinite(floor)) {
		console.error("coverage: --floor needs a number");
		process.exit(2);
	}
	report(out, floor, ignore, root, floorFile);
} else {
	console.error("usage: bun scripts/coverage.mjs merge <out> <in>... | report <profile> [--floor 95] [--floors path] [--ignore path] [--root .]");
	process.exit(2);
}
