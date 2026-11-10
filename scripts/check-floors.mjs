// The ratchet, as a check.
//
// `docs/engineering/standards.md` S10 says coverage and mutation floors are ratchets: they may
// only move up. That is a sentence until something compares the file with the one the base
// branch has — and the file is edited by the same pull request that would want to lower it. A
// bar that can be lowered to make a run green is a wish, which is what S10 says and what this
// makes false.
//
// What is checked, and what is deliberately not:
//
//   - a floor that falls is a failure, coverage and both mutation numbers;
//   - a package split is the one move that is not a fall: code and its tests leave a package
//     for named subpackages, so the parent's killed count drops while the sum the old floor
//     promised moves with them. The parent plus the new subpackages must still meet the old
//     killed floor, and every new package has its own row; a drop that is not accounted for
//     this way is still a failure.
//   - a floor line that disappears while its package is still in the tree is a failure, because
//     that is the same move written the other way;
//   - a floor that rises is reported and passes — the file is edited by hand, and the person who
//     did the work should not have to be told to raise it;
//   - the reason clause after `--` is not compared: rewriting why a package cannot reach 95% is
//     not lowering a bar, and S11 enforces its own shape at report time.
//
// Usage: bun scripts/check-floors.mjs [--base origin/main]
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

const COVERAGE = ".coveragefloors";
const MUTATION = ".mutationfloors";

function git(args) {
	return execFileSync("git", args, { encoding: "utf8" }).trimEnd();
}

function option(name) {
	const index = process.argv.indexOf(name);
	return index === -1 ? undefined : process.argv[index + 1];
}

/**
 * One entry per line — `<package> <floor>` or `<package> <efficacy> <killed>` — with `#` comments
 * and an optional `-- reason` after the numbers. A line that does not parse is an error rather
 * than a skip: a guard that ignores what it cannot read is a guard that passes.
 */
function parse(text, columns, file) {
	const rows = new Map();
	const shape = columns === 1 ? "<package> <floor>" : "<package> <efficacy> <killed>";
	text.split("\n").forEach((raw, index) => {
		const line = raw.trim();
		if (line === "" || line.startsWith("#")) return;
		const [head] = line.split("--");
		const parts = (head ?? "").trim().split(/\s+/);
		if (parts.length !== columns + 1) {
			throw new Error(`${file}:${index + 1}: expected ${shape}, got: ${line}`);
		}
		const numbers = parts.slice(1).map(Number);
		if (!numbers.every(Number.isFinite)) {
			throw new Error(`${file}:${index + 1}: not a number: ${line}`);
		}
		rows.set(parts[0], numbers);
	});
	return rows;
}

function readFromTree(file, columns) {
	return parse(readFileSync(file, "utf8"), columns, file);
}

function readFromBase(base, file, columns) {
	let text;
	try {
		text = git(["show", `${base}:${file}`]);
	} catch {
		throw new Error(
			`cannot read ${file} on ${base}: the comparison needs it.\n` +
				`  fetch it first: git fetch origin main\n` +
				`  or point at another base: --base <ref>`,
		);
	}
	return parse(text, columns, `${base}:${file}`);
}

/** Lower is worse, higher is a raise worth printing, equal is nothing. */
function compare(kind, name, was, now, violations, raises) {
	const fell = now.some((value, index) => value < was[index]);
	const rose = now.some((value, index) => value > was[index]);
	if (!fell && !rose) return;
	const moved = `${was.join("/")} -> ${now.join("/")} (${kind})`;
	if (fell) violations.push(`${name}: ${moved}`);
	else raises.push(`${name}: ${moved}`);
}

function judge(kind, base, head, violations, raises, splits, moves, exists) {
	let compared = 0;
	for (const [name, was] of base) {
		compared += 1;
		const now = head.get(name);
		if (now === undefined) {
			if (exists(name)) {
				violations.push(`${name}: the floor line is gone and the package is still in the tree`);
			}
			continue;
		}
		// A package split moves mutants into subpackages. The killed count is the number that
		// can be added up; efficacy is a ratio, and each moved package carries its own row. A
		// split needs a subpackage that is new to this tree, so a parent that simply lost
		// assertions cannot borrow an old neighbour's count.
		if (kind === "mutation" && now[1] < was[1] && hasNewDescendant(name, head, base)) {
			const moved = subpackageKilled(name, head);
			if (now[1] + moved >= was[1]) {
				splits.push(`${name}: ${was.join("/")} -> ${now.join("/")} + ${moved} in subpackages (mutation)`);
				continue;
			}
		}
		// Code can also move up: a helper lifted out of a driver into the shared package brings
		// its tests and mutants with it. A child's fall is covered when its parent's killed
		// count rose by at least the same amount.
		if (kind === "mutation" && now[1] < was[1]) {
			const parent = ancestorFloor(name, base);
			if (parent !== undefined) {
				const wasParent = base.get(parent), nowParent = head.get(parent);
				if (wasParent && nowParent && nowParent[1] - wasParent[1] >= was[1] - now[1]) {
					moves.push(`${name}: ${was.join("/")} -> ${now.join("/")}, ${parent} rose ${wasParent[1]} -> ${nowParent[1]} (mutation)`);
					continue;
				}
			}
		}
		compare(kind, name, was, now, violations, raises);
	}
	return compared;
}

/** The nearest package above name that has a floor: where a moved helper would land. */
function ancestorFloor(name, base) {
	const parts = name.split("/");
	for (let end = parts.length - 1; end >= 1; end -= 1) {
		const candidate = parts.slice(0, end).join("/");
		if (base.has(candidate)) return candidate;
	}
	return undefined;
}

/** Whether a package gained a subpackage floor in this tree: the shape of a split. */
function hasNewDescendant(name, head, base) {
	for (const child of head.keys()) {
		if (child.startsWith(name + "/") && !base.has(child)) return true;
	}
	return false;
}

/** The killed floors of a package's subpackages: what the old parent floor used to include. */
function subpackageKilled(name, head) {
	let total = 0;
	for (const [child, values] of head) {
		if (child.startsWith(name + "/")) total += values[1] ?? 0;
	}
	return total;
}

const base = option("--base") ?? "origin/main";

let baseCoverage;
let baseMutation;
let headCoverage;
let headMutation;
try {
	headCoverage = readFromTree(COVERAGE, 1);
	headMutation = readFromTree(MUTATION, 2);
	baseCoverage = readFromBase(base, COVERAGE, 1);
	baseMutation = readFromBase(base, MUTATION, 2);
} catch (error) {
	console.error(`floors: ${error.message}`);
	process.exit(1);
}

// A package kept its floor line but left the tree: nothing to compare, nothing to say.
const inTree = (name) => existsSync(join("gateway", name));

const violations = [];
const raises = [];
const splits = [];
const moves = [];
const coverageCount = judge("coverage", baseCoverage, headCoverage, violations, raises, splits, moves, inTree);
const mutationCount = judge("mutation", baseMutation, headMutation, violations, raises, splits, moves, inTree);

if (violations.length > 0) {
	console.error(`floors in ${COVERAGE} and ${MUTATION} may only move up (standards.md S10):`);
	for (const violation of violations) console.error(`  - ${violation}`);
	console.error(
		`\nIf the drop is the honest number, the fix is a test or a deletion — not the floor.\n` +
			`The alternative rejected in S10: a bar that can be lowered is a bar nobody has to meet.`,
	);
	process.exit(1);
}

const raiseText = raises.length === 0 ? "none moved" : `${raises.length} raised`;
console.log(
	`floors: ${coverageCount} coverage and ${mutationCount} mutation packages, compared with ${base}, ${raiseText}${splits.length === 0 ? "" : `, ${splits.length} split`}${moves.length === 0 ? "" : `, ${moves.length} move`}`,
);
for (const raise of raises) console.log(`  raised  ${raise}`);
for (const split of splits) console.log(`  split   ${split}`);
for (const move of moves) console.log(`  moved   ${move}`);
