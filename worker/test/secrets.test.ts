import { describe, expect, test } from "bun:test";
import { parseSecrets, secretSection } from "../src/secrets.ts";
import { formatList } from "../src/secret-tool.ts";

describe("the secrets control sends", () => {
	test("a JSON array of names, descriptions, hosts, and delivery", () => {
		const secrets = parseSecrets(
			JSON.stringify([
				{
					name: "GITHUB_TOKEN",
					description: "read-only token",
					hosts: ["api.github.com"],
					delivery: "substitute",
				},
				{ name: "DB_PASSWORD", description: "", hosts: [], delivery: "environment" },
			]),
		);
		expect(secrets).toEqual([
			{
				name: "GITHUB_TOKEN",
				description: "read-only token",
				hosts: ["api.github.com"],
				delivery: "substitute",
				placeholder: "$MSB_GITHUB_TOKEN",
			},
			{
				name: "DB_PASSWORD",
				description: "",
				hosts: [],
				delivery: "environment",
				placeholder: "",
			},
		]);
	});

	test("nothing, rather than a crash, when the answer is malformed", () => {
		expect(parseSecrets(undefined)).toEqual([]);
		expect(parseSecrets("")).toEqual([]);
		expect(parseSecrets("not json")).toEqual([]);
		expect(parseSecrets(`{"name":"TOKEN"}`)).toEqual([]);
		expect(parseSecrets(`[{"description":"no name"}]`)).toEqual([]);
	});

	test("only the fields it understands, with a safe delivery default", () => {
		const secrets = parseSecrets(
			JSON.stringify([{ name: "TOKEN", hosts: ["api.example.com", 3], delivery: "other" }]),
		);
		expect(secrets).toEqual([
			{
				name: "TOKEN",
				description: "",
				hosts: ["api.example.com"],
				delivery: "substitute",
				placeholder: "$MSB_TOKEN",
			},
		]);
	});
});

describe("the prompt section", () => {
	test("is absent when there are no secrets", () => {
		expect(secretSection([])).toBeUndefined();
	});

	test("names each secret and what a name is not", () => {
		const section = secretSection([
			{
				name: "GITHUB_TOKEN",
				description: "read-only token",
				hosts: ["api.github.com", "github.com"],
				delivery: "substitute",
				placeholder: "$MSB_GITHUB_TOKEN",
			},
			{
				name: "DB_PASSWORD",
				description: "the project database",
				hosts: [],
				delivery: "environment",
				placeholder: "",
			},
		]);
		expect(section).toContain("never print a secret");
		expect(section).toContain(
			"GITHUB_TOKEN is set to the literal $MSB_GITHUB_TOKEN; use $GITHUB_TOKEN in a shell or send $MSB_GITHUB_TOKEN directly for api.github.com, github.com over HTTPS.",
		);
		expect(section).toContain("$MSB_NAME is not itself a variable");
		expect(section).toContain(
			"DB_PASSWORD: the project database The sandbox environment holds the real value.",
		);
	});

	test("the list names the variable that holds the placeholder, not a variable named after it", () => {
		const list = formatList({
			secrets: [
				{
					name: "GITHUB_PAT",
					description: "the release token",
					hosts: ["api.github.com"],
					delivery: "substitute",
					status: "delivered",
					placeholder: "$MSB_GITHUB_PAT",
				},
			],
		});
		expect(list).toContain("GITHUB_PAT holds the literal $MSB_GITHUB_PAT");
		expect(list).not.toContain("the env holds");
	});
});
