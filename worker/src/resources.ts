/**
 * The workspace's resources as the worker holds them: skills, the agent catalog, the rules file,
 * and the commands. Everything but the skills is reloaded on demand, so editing a file does not
 * need a restart. See docs/adr/0004-resource-overlay.md.
 */
import { join } from "node:path";
import {
	COMMAND_DIRS,
	loadCatalog,
	resolveAgent,
	resourceFolders,
	type Catalog,
	type layerRoots,
} from "./config.ts";
import { loadCommands, type Command as SlashCommand } from "./commands.ts";
import type { Files } from "./files.ts";
import { log } from "./protocol.ts";
import { loadSkills, type Skill } from "./skills.ts";
import type { CommandTable, WorkerContext } from "./worker.ts";

type Roots = ReturnType<typeof layerRoots>;

export interface Resources {
	readonly skills: Skill[];
	catalog: Catalog;
	/**
	 * The workspace's rules file. It is its own prompt section rather than more
	 * `instructions`, so the context inspector can show it and diff it between requests.
	 */
	rules: string | undefined;
	commands: SlashCommand[];
	/** Read the catalog, the rules and the commands again. */
	reload(): Promise<void>;
}

export async function loadResources(
	roots: Roots,
	files: Files,
	workspace: string,
): Promise<Resources> {
	const loadRules = async (): Promise<string | undefined> => {
		const text = (await files.read(join(workspace, "AGENTS.md")))?.trim() ?? "";
		return text === "" ? undefined : text;
	};
	const skills = await loadSkills(resourceFolders(roots, ["skills"]), files);
	const catalog = await loadCatalog(roots, files);
	reportUnknownKeys(catalog);
	const resources: Resources = {
		skills,
		catalog,
		rules: await loadRules(),
		commands: await loadCommands(resourceFolders(roots, COMMAND_DIRS), files),
		async reload() {
			resources.catalog = await loadCatalog(roots, files);
			reportUnknownKeys(resources.catalog);
			resources.rules = await loadRules();
			resources.commands = await loadCommands(resourceFolders(roots, COMMAND_DIRS), files);
		},
	};
	return resources;
}

/** A typo in frontmatter is otherwise indistinguishable from a key that works. */
function reportUnknownKeys(catalog: Catalog): void {
	for (const agent of catalog.agents) {
		if (agent.unknownKeys !== undefined && agent.unknownKeys.length > 0) {
			log(
				`agent ${agent.name}: unknown frontmatter keys ${agent.unknownKeys.join(", ")} (${agent.source})`,
			);
		}
	}
}

export function resourceCommands({
	resources,
}: WorkerContext): Pick<CommandTable, "agents" | "reload"> {
	return {
		agents: async () => ({
			defaults: resources.catalog.defaults,
			skills: resources.skills.map((skill) => ({
				name: skill.name,
				description: skill.description,
			})),
			agents: resources.catalog.agents.map((agent) => ({
				name: agent.name,
				description: agent.description,
				source: agent.source,
				layer: agent.layer,
				resolved: resolveAgent(resources.catalog, agent.name),
			})),
		}),
		reload: async () => {
			await resources.reload();
			return {
				agents: resources.catalog.agents.length,
				commands: resources.commands.length,
				rules: resources.rules !== undefined,
			};
		},
	};
}
